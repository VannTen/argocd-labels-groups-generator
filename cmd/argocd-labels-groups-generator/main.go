package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicinformer "k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/VannTen/argocd-labels-groups-generator/internal/generator"
)

type GVK struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

type Input struct {
	Namespace  string `json:"namespace"`
	GVK        GVK    `json:"gvk"`
	Selector   string `json:"selector,omitempty"`
	GroupBy    string `json:"groupBy"`
	ValuesFrom string `json:"valuesFrom"`
}

type Server struct {
	informer cache.SharedIndexInformer
	ready    atomic.Bool
}

type Request struct {
	Input struct {
		Parameters Input `json:"parameters"`
	} `json:"input"`
}

type Output struct {
	Parameters []map[string]any `json:"parameters"`
}

type Response struct {
	Output Output `json:"output"`
}

func parseGVR(s string) (schema.GroupVersionResource, error) {
	parts := strings.Split(s, "/")

	switch len(parts) {
	case 2: // Core API: v1/pods
		return schema.GroupVersionResource{
			Version:  parts[0],
			Resource: parts[1],
		}, nil
	case 3: // Grouped API: argoproj.io/v1alpha1/applications
		return schema.GroupVersionResource{
			Group:    parts[0],
			Version:  parts[1],
			Resource: parts[2],
		}, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf(
			"invalid GVR %q: expected version/resource or group/version/resource", s,
		)
	}
}

func (s *Server) getParams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Parse and validate the request parameters.
	params := req.Input.Parameters

	selector, err := labels.Parse(params.Selector)
	if err != nil {
		http.Error(w, "invalid selector: "+err.Error(), http.StatusBadRequest)
		return
	}

	objects := make([]*unstructured.Unstructured, 0, len(s.informer.GetStore().List()))

	for _, obj := range s.informer.GetStore().List() {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}

		objectLabels := u.GetLabels()
		if selector.Matches(labels.Set(objectLabels)) &&
			objectLabels[params.GroupBy] != "" &&
			objectLabels[params.ValuesFrom] != "" {
			objects = append(objects, u)
		}
	}

	response := Response{}
	response.Output.Parameters = generator.Generate(objects, params.GroupBy, params.ValuesFrom)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encoding response: %v", err)
	}
}

func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) healthyHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func main() {
	if err := run(); err != nil {
		log.Printf("ERROR: %v", err)
		os.Exit(1)
	}
}

func run() error {
	gvrString := flag.String(
		"gvr",
		os.Getenv("RESOURCE_GVR"),
		"Resource to watch (e.g. argoproj.io/v1alpha1/applications or v1/pods)",
	)
	namespace := flag.String(
		"namespace",
		os.Getenv("NAMESPACE"),
		"Namespace to watch (default: all namespaces)",
	)
	watchSelector := flag.String(
		"watch-selector",
		os.Getenv("WATCH_SELECTOR"),
		"Fixed label selector applied by the API server",
	)
	kubeconfig := flag.String(
		"kubeconfig",
		os.Getenv("KUBECONFIG"),
		"Path to kubeconfig (default: in-cluster config or default kubeconfig)",
	)
	contextName := flag.String(
		"context",
		os.Getenv("KUBE_CONTEXT"),
		"Kubeconfig context to use",
	)
	flag.Parse()

	if *gvrString == "" {
		return fmt.Errorf("gvr is required")
	}
	if *namespace == "" {
		log.Print("WARNING: no namespace configured; watching all namespaces")
	}

	gvr, err := parseGVR(*gvrString)
	if err != nil {
		return err
	}

	var config *rest.Config

	if *contextName == "" {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		if *kubeconfig != "" {
			loadingRules.ExplicitPath = *kubeconfig
		}

		overrides := &clientcmd.ConfigOverrides{
			CurrentContext: *contextName,
		}
		config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			overrides,
		).ClientConfig()
	}

	if err != nil {
		return fmt.Errorf("loading Kubernetes config: %w", err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	signalCtx, stopSignals := signal.NotifyContext(
		context.Background(),
		syscall.SIGTERM,
		os.Interrupt,
	)
	defer stopSignals()

	informerCtx, cancelInformer := context.WithCancel(context.Background())
	defer cancelInformer()

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		client,
		10*time.Minute,
		*namespace,
		func(options *metav1.ListOptions) {
			options.LabelSelector = *watchSelector
		},
	)

	informer := factory.ForResource(gvr).Informer()
	factory.Start(informerCtx.Done())

	if !cache.WaitForCacheSync(signalCtx.Done(), informer.HasSynced) {
		if signalCtx.Err() != nil {
			return nil
		}
		return fmt.Errorf("failed waiting for resource informer cache")
	}

	server := &Server{informer: informer}
	server.ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", server.healthyHandler)
	mux.HandleFunc("/ready", server.readyHandler)
	mux.HandleFunc("/api/v1/getparams.execute", server.getParams)

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":4355"
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("HTTP server: %w", err)
			return
		}
		serveErr <- nil
	}()

	log.Printf("watching %s in namespace %q", gvr, *namespace)
	log.Printf("listening on %s", addr)

	select {
	case <-signalCtx.Done():
		log.Print("shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			log.Printf("server stopped: %v", err)
		}
	}

	// Remove this pod from readiness before draining requests.
	server.ready.Store(false)

	// Best-effort time for Kubernetes endpoint changes to propagate.
	//	time.Sleep(5 * time.Second)

	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancelShutdown()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("shutting down HTTP server: %w", err)
	}

	// The informer is stopped by cancelInformer when run returns.
	log.Print("shutdown complete")
	return nil
}
