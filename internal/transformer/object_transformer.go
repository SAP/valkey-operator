/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package transformer

import (
	"sync"

	"github.com/sap/component-operator-runtime/pkg/manifests"
	"github.com/sap/component-operator-runtime/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// objectTransformer post-processes the generated StatefulSets. It also implements
// ParameterTransformer to capture the component's spec.annotations during the
// parameter phase (object transformers do not receive the parameters), so they can
// be merged onto the StatefulSet metadata during the object phase.
type objectTransformer struct {
	mutex       sync.Mutex
	annotations map[string]map[string]string
}

var (
	_ manifests.ParameterTransformer = &objectTransformer{}
	_ manifests.ObjectTransformer    = &objectTransformer{}
)

func NewObjectTransformer() *objectTransformer {
	return &objectTransformer{annotations: make(map[string]map[string]string)}
}

func (t *objectTransformer) TransformParameters(namespace string, name string, parameters types.Unstructurable) (types.Unstructurable, error) {
	annotations := annotationsFromParameters(parameters)
	key := namespace + "/" + name
	t.mutex.Lock()
	if len(annotations) == 0 {
		delete(t.annotations, key)
	} else {
		t.annotations[key] = annotations
	}
	t.mutex.Unlock()
	return parameters, nil
}

func (t *objectTransformer) TransformObjects(namespace string, name string, objects []client.Object) ([]client.Object, error) {
	key := namespace + "/" + name
	t.mutex.Lock()
	annotations := t.annotations[key]
	delete(t.annotations, key)
	t.mutex.Unlock()

	for i := 0; i < len(objects); i++ {
		if statefulSet := asStatefulSet(objects[i]); statefulSet != nil {
			if len(statefulSet.Spec.Template.Spec.TopologySpreadConstraints) == 0 {
				statefulSet.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "kubernetes.io/hostname",
						WhenUnsatisfiable:  corev1.ScheduleAnyway,
						NodeAffinityPolicy: &[]corev1.NodeInclusionPolicy{corev1.NodeInclusionPolicyHonor}[0],
						NodeTaintsPolicy:   &[]corev1.NodeInclusionPolicy{corev1.NodeInclusionPolicyHonor}[0],
					},
				}
			}
			for j := 0; j < len(statefulSet.Spec.Template.Spec.TopologySpreadConstraints); j++ {
				constraint := &statefulSet.Spec.Template.Spec.TopologySpreadConstraints[j]
				if constraint.LabelSelector == nil && len(constraint.MatchLabelKeys) == 0 {
					constraint.LabelSelector = statefulSet.Spec.Selector
					constraint.MatchLabelKeys = []string{"controller-revision-hash"}
				}
			}
			if len(annotations) > 0 {
				merged := statefulSet.GetAnnotations()
				if merged == nil {
					merged = make(map[string]string, len(annotations))
				}
				for k, v := range annotations {
					merged[k] = v
				}
				statefulSet.SetAnnotations(merged)
			}
			objects[i] = asUnstructurable(statefulSet)
		}
	}
	// TODO: set persistentVolumeClaimRetentionPolicy to Delete (available from 1.27; unless chart natively supports it)
	return objects, nil
}

func annotationsFromParameters(parameters types.Unstructurable) map[string]string {
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

func asStatefulSet(object client.Object) *appsv1.StatefulSet {
	if statefulSet, ok := object.(*appsv1.StatefulSet); ok {
		return statefulSet
	}
	if object, ok := object.(*unstructured.Unstructured); ok && (object.GetObjectKind().GroupVersionKind() == schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}) {
		statefulSet := &appsv1.StatefulSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, statefulSet); err != nil {
			panic(err)
		}
		return statefulSet
	}
	return nil
}

func asUnstructurable(object client.Object) *unstructured.Unstructured {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: m}
}
