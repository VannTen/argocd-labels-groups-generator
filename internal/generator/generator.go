package generator

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func groupBy[T any, K comparable, V comparable](
	items []T,
	key func(T) K,
	value func(T) V,
) map[K]map[V]struct{} {
	groups := make(map[K]map[V]struct{})

	for _, item := range items {
		k := key(item)

		if groups[k] == nil {
			groups[k] = make(map[V]struct{})
		}

		groups[k][value(item)] = struct{}{}
	}

	return groups
}

func Generate(
	metadatas []*unstructured.Unstructured,
	groupKey, collectKey string,
) []map[string]any {

	groups := groupBy(
		metadatas,
		func(obj *unstructured.Unstructured) string {
			return obj.GetLabels()[groupKey]
		},
		func(obj *unstructured.Unstructured) string {
			return obj.GetLabels()[collectKey]
		})

	// Make output deterministic.
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	parameters := make([]map[string]any, 0, len(groups))

	for _, name := range groupNames {
		values := make([]string, 0, len(groups[name]))
		for value := range groups[name] {
			values = append(values, value)
		}
		sort.Strings(values)

		parameters = append(parameters, map[string]any{
			groupKey:   name,
			collectKey: values,
		})
	}

	return parameters
}
