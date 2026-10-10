/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sap/component-operator-runtime/pkg/component"
	"github.com/sap/component-operator-runtime/pkg/manifests"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
)

func TestImageParameters(t *testing.T) {
	transformer, err := manifests.NewTemplateParameterTransformer(data, "data/parameters.yaml")
	NewWithT(t).Expect(err).NotTo(HaveOccurred())

	render := func(t *testing.T, spec *operatorv1alpha1.ValkeySpec) map[string]any {
		t.Helper()
		parameters, err := transformer.TransformParameters("testns", "test", spec)
		NewWithT(t).Expect(err).NotTo(HaveOccurred())
		return parameters.ToUnstructured()
	}

	// pull secrets are pod-level, so they stay global. the registry cannot: global.imageRegistry
	// would win over every per-image registry
	t.Run("registry is per image, pull secrets stay global", func(t *testing.T) {
		g := NewWithT(t)
		values := render(t, &operatorv1alpha1.ValkeySpec{
			Replicas: 1,
			Image: &operatorv1alpha1.ImageProperties{
				Registry:    "registry.example.com",
				Repository:  "mirror/valkey",
				PullSecrets: []string{"my-pull-secret"},
			},
		})

		g.Expect(values["image"]).To(HaveKeyWithValue("registry", "registry.example.com"))
		g.Expect(values["image"]).To(HaveKeyWithValue("repository", "mirror/valkey"))
		g.Expect(values["global"]).NotTo(HaveKey("imageRegistry"))
		g.Expect(values["global"]).To(HaveKeyWithValue("imagePullSecrets", []any{"my-pull-secret"}))
	})

	// the sidecars default to spec.image. the chart has no global pull policy, so pullPolicy is
	// repeated per image
	t.Run("sidecars inherit from spec.image", func(t *testing.T) {
		g := NewWithT(t)
		values := render(t, &operatorv1alpha1.ValkeySpec{
			Replicas: 3,
			Version:  "8.1.3-debian-12-r0",
			Image: &operatorv1alpha1.ImageProperties{
				Registry:   "registry.example.com",
				PullPolicy: "Always",
			},
			Sentinel: &operatorv1alpha1.SentinelProperties{Enabled: true},
			Metrics:  &operatorv1alpha1.MetricsProperties{Enabled: true},
		})

		g.Expect(values["image"]).To(HaveKeyWithValue("tag", "8.1.3-debian-12-r0"))
		g.Expect(values["image"]).To(HaveKeyWithValue("pullPolicy", "Always"))
		g.Expect(values["sentinel"]).To(HaveKeyWithValue("image", map[string]any{
			"registry":   "registry.example.com",
			"repository": "bitnamilegacy/valkey-sentinel",
			"tag":        "8.1.3-debian-12-r0",
			"pullPolicy": "Always",
		}))
		// the exporter takes the registry but keeps its own version
		g.Expect(values["metrics"]).To(HaveKeyWithValue("image", map[string]any{
			"registry":   "registry.example.com",
			"repository": "bitnamilegacy/redis-exporter",
			"pullPolicy": "Always",
		}))
	})

	// every image is addressable on its own, so a mirror can use any layout
	t.Run("each image takes a full path", func(t *testing.T) {
		g := NewWithT(t)
		values := render(t, &operatorv1alpha1.ValkeySpec{
			Replicas: 3,
			Image: &operatorv1alpha1.ImageProperties{
				Registry:   "one.example.com/dockerhub",
				Repository: "mycorp/valkey",
				Tag:        "8.1.3-debian-12-r0",
			},
			Sentinel: &operatorv1alpha1.SentinelProperties{
				Enabled: true,
				Image: &operatorv1alpha1.ImageOverride{
					Registry:   "two.example.com",
					Repository: "mycorp/valkey-sentinel",
					Tag:        "8.1.4-debian-12-r0",
				},
			},
			Metrics: &operatorv1alpha1.MetricsProperties{
				Enabled: true,
				Image: &operatorv1alpha1.ImageOverride{
					Registry:   "three.example.com",
					Repository: "mycorp/redis-exporter",
					Tag:        "1.67.0-debian-12-r0",
				},
			},
		})

		g.Expect(values["image"]).To(Equal(map[string]any{
			"registry":   "one.example.com/dockerhub",
			"repository": "mycorp/valkey",
			"tag":        "8.1.3-debian-12-r0",
		}))
		g.Expect(values["sentinel"]).To(HaveKeyWithValue("image", map[string]any{
			"registry":   "two.example.com",
			"repository": "mycorp/valkey-sentinel",
			"tag":        "8.1.4-debian-12-r0",
		}))
		g.Expect(values["metrics"]).To(HaveKeyWithValue("image", map[string]any{
			"registry":   "three.example.com",
			"repository": "mycorp/redis-exporter",
			"tag":        "1.67.0-debian-12-r0",
		}))
	})

	// default repository, plus the waiver the chart needs for relocated repositories
	t.Run("defaults", func(t *testing.T) {
		g := NewWithT(t)
		values := render(t, &operatorv1alpha1.ValkeySpec{Replicas: 1})

		g.Expect(values["image"]).To(Equal(map[string]any{"repository": "bitnamilegacy/valkey"}))
		g.Expect(values["global"]).To(Equal(map[string]any{
			"security": map[string]any{"allowInsecureImages": true},
		}))
	})

	t.Run("image.tag wins over version", func(t *testing.T) {
		values := render(t, &operatorv1alpha1.ValkeySpec{
			Replicas: 1,
			Version:  "8.1.3-debian-12-r0",
			Image:    &operatorv1alpha1.ImageProperties{Tag: "8.1.4-debian-12-r0"},
		})

		NewWithT(t).Expect(values["image"]).To(HaveKeyWithValue("tag", "8.1.4-debian-12-r0"))
	})

	// spec values are interpolated into yaml. unquoted, any of them could inject arbitrary chart
	// values, e.g. turn authentication off
	t.Run("interpolated values cannot inject chart values", func(t *testing.T) {
		size := resource.MustParse("1Gi")

		// the first three add a sibling key at 0, 2 and 4 spaces of indentation, reaching a value at
		// any depth. the last overwrites a key instead of adding one.
		// payloads must not contain "<no value>", which the renderer strips after quoting
		payloads := []string{
			"\nauth:\n  enabled: false",
			"\n  imageRegistry: evil.example.com",
			"\n    pullPolicy: Never",
			"\nfullnameOverride: valkey-other",
		}

		// takes the payload back out of the strings it landed in. a render that quoted every value
		// normalizes back to the payload-free one. a render that injected does not: it has extra
		// keys, overwritten ones, or no longer parses as yaml
		var strip func(value any, payload string) any
		strip = func(value any, payload string) any {
			switch value := value.(type) {
			case map[string]any:
				stripped := make(map[string]any, len(value))
				for key, item := range value {
					stripped[key] = strip(item, payload)
				}
				return stripped
			case []any:
				stripped := make([]any, len(value))
				for i, item := range value {
					stripped[i] = strip(item, payload)
				}
				return stripped
			case string:
				return strings.ReplaceAll(value, payload, "")
			}
			return value
		}

		// the payload goes into every field of the block, so one unquoted field fails the case
		for name, build := range map[string]func(payload string) *operatorv1alpha1.ValkeySpec{
			"server image": func(payload string) *operatorv1alpha1.ValkeySpec {
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, Image: &operatorv1alpha1.ImageProperties{
					Registry:   "registry.example.com" + payload,
					Repository: "mirror/valkey" + payload,
					Tag:        "8.1.3" + payload,
				}}
			},
			"sentinel image": func(payload string) *operatorv1alpha1.ValkeySpec {
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, Sentinel: &operatorv1alpha1.SentinelProperties{
					Enabled: true,
					Image: &operatorv1alpha1.ImageOverride{
						Registry:   "two.example.com" + payload,
						Repository: "mirror/valkey-sentinel" + payload,
						Tag:        "8.1.3" + payload,
					},
				}}
			},
			"metrics image": func(payload string) *operatorv1alpha1.ValkeySpec {
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, Metrics: &operatorv1alpha1.MetricsProperties{
					Enabled: true,
					Image: &operatorv1alpha1.ImageOverride{
						Registry:   "three.example.com" + payload,
						Repository: "mirror/redis-exporter" + payload,
						Tag:        "1.67.0" + payload,
					},
				}}
			},
			"storage class": func(payload string) *operatorv1alpha1.ValkeySpec {
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, Persistence: &operatorv1alpha1.PersistenceProperties{
					Enabled:      true,
					Size:         &size,
					StorageClass: "fast" + payload,
				}}
			},
			"binding secret name": func(payload string) *operatorv1alpha1.ValkeySpec {
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, Binding: &operatorv1alpha1.BindingProperties{
					SecretName: "my-binding" + payload,
				}}
			},
			"priority class name": func(payload string) *operatorv1alpha1.ValkeySpec {
				priorityClassName := "high" + payload
				return &operatorv1alpha1.ValkeySpec{Replicas: 1, KubernetesPodProperties: component.KubernetesPodProperties{
					PriorityClassName: &priorityClassName,
				}}
			},
		} {
			t.Run(name, func(t *testing.T) {
				g := NewWithT(t)
				want := render(t, build(""))

				for _, payload := range payloads {
					g.Expect(strip(render(t, build(payload)), payload)).To(Equal(want))
				}
			})
		}
	})
}
