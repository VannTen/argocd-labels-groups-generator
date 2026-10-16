# argocd-labels-groups-generator

An Argo CD ApplicationSet HTTP plugin that groups Kubernetes objects by one
label and collects the distinct values of another label. It watches a
configured Kubernetes resource through a shared informer and answers
ApplicationSet plugin requests from its cache.

## Example use case: cluster-aware GitOps promotion

Original use case for this project: using an Application Set to generate one
PromotionStrategy  (see [Gitops
Promoter](https://gitops-promoter.readthedocs.io/en/latest/)) per application
on a set of environments/Kubernetes cluster, where not all applications are
deployed on each cluster, but the promotion graph is shared.


e, ask for the distinct `cluster` label values grouped by `application`:

```json
{
  "input": {
    "parameters": {
      "valuesFrom": "cluster",
      "groupBy": "application",
      "selector": "<optionnal label selector to restrict considered resources>"
    }
  }
}
```

The response has one entry per application, for example:

```json
{
  "output": {
    "parameters": [
      {
        "application": "cert-manager",
        "cluster": ["env_pp", "env_pr"]
      },
      {
        "application": "ingress-controller",
        "cluster": ["env_dev", "env_qa", "env_pp", "env_pr"]
      }
    ]
  }
}
```

The promoter can then conditionally construct its DAG: e.g. create or enable an
edge only when the relevant application exists in the source and/or target
cluster. The plugin reports observed membership; the promotion policy remains
the responsibility of the application created from the applicationset (using jsonnet to prune a fixed DAG based on the `cluster` parameter)

## Configuration

The process must be able to list and watch the configured resource. In-cluster credentials are used in Kubernetes; local development can use a kubeconfig.

| Setting | Purpose |
| --- | --- |
| `RESOURCE_GVR` / `--gvr` | Resource to watch, e.g. `argoproj.io/v1alpha1/applications` |
| `NAMESPACE` / `--namespace` | Namespace to watch; empty means all namespaces |
| `WATCH_SELECTOR` / `--watch-selector` | Optional fixed label selector applied by the API server |
| `LISTEN_ADDR` | HTTP listen address; defaults to `:4355` |

The request's `selector` further filters cached objects per request. `groupBy` and `valuesFrom` name the labels to group. Objects missing either label are ignored. The returned groups and values are sorted for deterministic output.

Endpoints:

- `POST /api/v1/getparams.execute` — ApplicationSet plugin request.
- `GET /healthy` — liveness probe.
- `GET /ready` — readiness probe; returns `503` until the informer cache has synced and while shutting down.

Example local request (adjust the URL and request file as needed):

```sh
curl -fsS -H 'Content-Type: application/json' \
  -d @request.json http://localhost:4355/api/v1/getparams.execute | jq .
```
