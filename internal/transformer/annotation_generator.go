/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package transformer

import (
	"context"

	"github.com/sap/component-operator-runtime/pkg/manifests"
	componentoperatorruntimetypes "github.com/sap/component-operator-runtime/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// annotationGenerator wraps a generator and merges the component's spec.annotations
// onto the metadata of every generated StatefulSet. It is applied in the operator
// (not the vendored chart) so the behaviour is independent of the chart templates.
type annotationGenerator struct {
	generator manifests.Generator
}

func NewAnnotationGenerator(generator manifests.Generator) *annotationGenerator {
	return &annotationGenerator{generator: generator}
}

func (g *annotationGenerator) Generate(ctx context.Context, namespace string, name string, parameters componentoperatorruntimetypes.Unstructurable) ([]client.Object, error) {
	objects, err := g.generator.Generate(ctx, namespace, name, parameters)
	if err != nil {
		return nil, err
	}
	annotations := annotationsFromParameters(parameters)
	if len(annotations) == 0 {
		return objects, nil
	}
	for i := 0; i < len(objects); i++ {
		if statefulSet := asStatefulSet(objects[i]); statefulSet != nil {
			merged := statefulSet.GetAnnotations()
			if merged == nil {
				merged = make(map[string]string, len(annotations))
			}
			for key, value := range annotations {
				merged[key] = value
			}
			statefulSet.SetAnnotations(merged)
			objects[i] = asUnstructurable(statefulSet)
		}
	}
	return objects, nil
}

func annotationsFromParameters(parameters componentoperatorruntimetypes.Unstructurable) map[string]string {
	raw, ok := parameters.ToUnstructured()["annotations"].(map[string]any)
	if !ok {
		return nil
	}
	annotations := make(map[string]string, len(raw))
	for key, value := range raw {
		if s, ok := value.(string); ok {
			annotations[key] = s
		}
	}
	return annotations
}
