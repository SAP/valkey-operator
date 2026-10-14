/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package transformer

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
)

// metricsContainerName is the name the Bitnami valkey chart gives the metrics
// exporter sidecar in every StatefulSet that carries it.
const metricsContainerName = "metrics"

type objectTransformer struct {
	client client.Client
	// defaultExporterVersion is the redis_exporter tag applied to the metrics
	// container when the Valkey CR does not set spec.metrics.exporterVersion. It is
	// derived from the chart's own Chart.yaml at startup, so it tracks the chart and
	// is never hardcoded in parameters.yaml. May be empty.
	defaultExporterVersion string
}

func NewObjectTransformer(c client.Client, defaultExporterVersion string) *objectTransformer {
	return &objectTransformer{client: c, defaultExporterVersion: defaultExporterVersion}
}

func (t *objectTransformer) TransformObjects(namespace string, name string, objects []client.Object) ([]client.Object, error) {
	exporterVersion := t.exporterVersion(namespace, name)
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
			normalizeMetricsContainer(statefulSet, exporterVersion)
			objects[i] = asUnstructurable(statefulSet)
		}
	}
	// TODO: set persistentVolumeClaimRetentionPolicy to Delete (available from 1.27; unless chart natively supports it)
	return objects, nil
}

// exporterVersion resolves the redis_exporter tag to use for the given Valkey CR:
// spec.metrics.exporterVersion when the user set it, otherwise the chart-derived
// default. Reading the CR keeps the version out of parameters.yaml entirely (which
// must not carry hardcoded image tags), while still tracking the chart's default.
// On any read error it falls back to the default (never blocks reconciliation).
func (t *objectTransformer) exporterVersion(namespace string, name string) string {
	valkey := &operatorv1alpha1.Valkey{}
	if err := t.client.Get(context.Background(), apitypes.NamespacedName{Namespace: namespace, Name: name}, valkey); err == nil {
		if valkey.Spec.Metrics != nil && strings.TrimSpace(valkey.Spec.Metrics.ExporterVersion) != "" {
			return strings.TrimSpace(valkey.Spec.Metrics.ExporterVersion)
		}
	}
	return t.defaultExporterVersion
}

// normalizeMetricsContainer adapts the chart's metrics exporter sidecar to the
// scratch-based oliver006/redis_exporter image.
//
// The Bitnami valkey chart hardcodes the metrics command as a "/bin/bash -c ..."
// wrapper (in sentinel mode it cannot be overridden via Helm values at all). The
// oliver006/redis_exporter image is built FROM scratch and has no shell, so the
// wrapper fails. We rewrite the command to invoke the binary directly for every
// mode. The password the wrapper used to read from a file is instead injected as
// the REDIS_PASSWORD env var via parameters.yaml.
//
// parameters.yaml only sets the metrics image repository (oliver006/redis_exporter),
// never a tag; the chart would otherwise leave its own default tag in place. We
// therefore strip whatever tag/digest the chart rendered and set exporterVersion
// (resolved from the CR or the chart-derived default). When exporterVersion is
// empty we leave the image untouched.
func normalizeMetricsContainer(statefulSet *appsv1.StatefulSet, exporterVersion string) {
	containers := statefulSet.Spec.Template.Spec.Containers
	for i := range containers {
		if containers[i].Name != metricsContainerName {
			continue
		}
		containers[i].Command = []string{"/redis_exporter"}
		containers[i].Args = nil
		if exporterVersion != "" {
			containers[i].Image = stripImageTagOrDigest(containers[i].Image) + ":" + exporterVersion
		}
	}
}

// stripImageTagOrDigest returns the image reference without its ':tag' or '@digest'
// suffix, keeping any registry (including a registry with a port) and repository.
func stripImageTagOrDigest(image string) string {
	lastSlash := strings.LastIndex(image, "/")
	segment := image[lastSlash+1:]
	if i := strings.IndexAny(segment, ":@"); i >= 0 {
		return image[:lastSlash+1+i]
	}
	return image
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
