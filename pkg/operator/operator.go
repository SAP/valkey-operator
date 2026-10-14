/*
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package operator

import (
	"embed"
	"flag"
	"io/fs"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/sap/component-operator-runtime/pkg/component"
	"github.com/sap/component-operator-runtime/pkg/manifests"
	"github.com/sap/component-operator-runtime/pkg/manifests/helm"
	"github.com/sap/component-operator-runtime/pkg/operator"

	operatorv1alpha1 "github.com/sap/valkey-operator/api/v1alpha1"
	"github.com/sap/valkey-operator/internal/transformer"
)

const Name = "valkey-operator.cs.sap.com"

//go:embed all:data
var data embed.FS

type Options struct {
	Name       string
	FlagPrefix string
}

type Operator struct {
	options Options
}

var defaultOperator operator.Operator = New()

func GetName() string {
	return defaultOperator.GetName()
}

func InitScheme(scheme *runtime.Scheme) {
	defaultOperator.InitScheme(scheme)
}

func InitFlags(flagset *flag.FlagSet) {
	defaultOperator.InitFlags(flagset)
}

func ValidateFlags() error {
	return defaultOperator.ValidateFlags()
}

func GetUncacheableTypes() []client.Object {
	return defaultOperator.GetUncacheableTypes()
}

func Setup(mgr ctrl.Manager) error {
	return defaultOperator.Setup(mgr)
}

func New() *Operator {
	return NewWithOptions(Options{})
}

func NewWithOptions(options Options) *Operator {
	operator := &Operator{options: options}
	if operator.options.Name == "" {
		operator.options.Name = Name
	}
	return operator
}

func (o *Operator) GetName() string {
	return o.options.Name
}

func (o *Operator) InitScheme(scheme *runtime.Scheme) {
	utilruntime.Must(operatorv1alpha1.AddToScheme(scheme))
}

func (o *Operator) InitFlags(flagset *flag.FlagSet) {
	// Add logic to initialize flags (if running in a combined controller you might want to evaluate o.options.FlagPrefix).
}

func (o *Operator) ValidateFlags() error {
	// Add logic to validate flags (if running in a combined controller you might want to evaluate o.options.FlagPrefix).
	return nil
}

func (o *Operator) GetUncacheableTypes() []client.Object {
	// Add types which should bypass informer caching.
	return []client.Object{&operatorv1alpha1.Valkey{}}
}

func (o *Operator) Setup(mgr ctrl.Manager) error {
	parameterTransformer, err := manifests.NewTemplateParameterTransformer(data, "data/parameters.yaml")
	if err != nil {
		return errors.Wrap(err, "error initializing parameter transformer")
	}
	objectTransformer := transformer.NewObjectTransformer(mgr.GetClient(), readExporterVersionFromChart(data, "data/charts/valkey/Chart.yaml"))
	resourceGenerator, err := helm.NewTransformableHelmGenerator(
		data,
		"data/charts/valkey",
		mgr.GetClient(),
	)
	if err != nil {
		return errors.Wrap(err, "error initializing resource generator")
	}
	resourceGenerator.
		WithParameterTransformer(parameterTransformer).
		WithObjectTransformer(objectTransformer)

	// TODO: handle increases of persistence.size somehow (instead of making it immutable)
	// this would require to recreate the statefulset (since persistentVolumeClaimTemplate is immutable)
	// and to extend existing persistent volume claims (supposing that they are resizable)

	if err := component.NewReconciler[*operatorv1alpha1.Valkey](
		o.options.Name,
		resourceGenerator,
		component.ReconcilerOptions{},
	).WithPostReconcileHook(
		reconcileBinding,
	).SetupWithManager(mgr); err != nil {
		return errors.Wrapf(err, "unable to create controller")
	}
	operatorv1alpha1.NewWebhook().SetupWithManager(mgr)
	return nil
}

// readExporterVersionFromChart derives the default redis_exporter tag from the
// chart's own Chart.yaml annotations.images block (the "redis-exporter" entry).
// This lets the operator pin a sensible exporter version without hardcoding a tag
// in parameters.yaml, and it tracks the chart automatically on future bumps.
//
// The chart declares e.g. "docker.io/bitnami/redis-exporter:1.67.0-debian-12-r0";
// we extract "1.67.0" and return it with the "v" prefix that oliver006/redis_exporter
// tags use ("v1.67.0"). Any read/parse failure returns "" (non-fatal): the metrics
// image is then left untagged rather than blocking operator startup.
func readExporterVersionFromChart(fsys fs.FS, path string) string {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		return ""
	}
	var meta struct {
		Annotations struct {
			Images string `json:"images"`
		} `json:"annotations"`
	}
	if err := yaml.Unmarshal(raw, &meta); err != nil {
		return ""
	}
	var images []struct {
		Name  string `json:"name"`
		Image string `json:"image"`
	}
	if err := yaml.Unmarshal([]byte(meta.Annotations.Images), &images); err != nil {
		return ""
	}
	for _, img := range images {
		if img.Name != "redis-exporter" {
			continue
		}
		// img.Image is "<registry>/<repo>:<tag>"; take the tag after the last ':'.
		ref := img.Image
		if i := strings.LastIndex(ref, ":"); i >= 0 && i > strings.LastIndex(ref, "/") {
			tag := ref[i+1:]
			// Bitnami tags look like "1.67.0-debian-12-r0"; keep only the semver part.
			if j := strings.Index(tag, "-"); j >= 0 {
				tag = tag[:j]
			}
			if tag != "" {
				return "v" + tag
			}
		}
	}
	return ""
}
