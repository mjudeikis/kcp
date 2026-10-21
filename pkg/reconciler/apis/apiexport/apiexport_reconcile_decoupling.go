/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apiexport

import (
	"context"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/identity"
)

const (
	// WellKnownIdentityPrefix prefixes the identities of kcp's core APIExports
	// on fresh installations. No logical cluster is named "system", so no
	// user export can generate or pin an identity with this prefix.
	WellKnownIdentityPrefix = "system-"

	identityPinInvalidReason = "IdentityPinInvalid"
)

var (
	coreExportNamesOnce sync.Once
	coreExportNames     sets.Set[string]
)

// isCoreExport reports whether the export is one of kcp's core APIExports in
// the root workspace.
func isCoreExport(apiExport *apisv1alpha2.APIExport) bool {
	if logicalcluster.From(apiExport) != core.RootCluster {
		return false
	}
	coreExportNamesOnce.Do(func() {
		coreExportNames = sets.New(configrootphase0.CoreAPIExportNames()...)
	})
	return coreExportNames.Has(apiExport.Name)
}

// WellKnownIdentity returns the well-known identity of a core APIExport.
func WellKnownIdentity(exportName string) string {
	return WellKnownIdentityPrefix + exportName
}

func (c *controller) reconcile(ctx context.Context, apiExport *apisv1alpha2.APIExport) error {
	err := c.reconcileIdentityAndSlice(ctx, apiExport)
	if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.APIIdentityDecoupling) {
		c.reconcileStorage(apiExport)
	}
	return err
}

// reconcileDecoupledIdentity assigns and validates the export's identity
// under the APIIdentityDecoupling feature gate (enhancements KEP 0006). The
// identity is an opaque, owner-prefixed identifier assigned once; no identity
// secret is created or read for it. An export that already has an identity
// keeps it untouched, whatever its secret says. Core exports in root get
// well-known identities, an export may pin its identity through
// spec.identity.value, and an export without an identity but with a
// pre-created secretRef is hashed from the secret one last time, so flows
// that pre-create secrets to pin an identity keep landing on it during the
// deprecation window.
func (c *controller) reconcileDecoupledIdentity(ctx context.Context, clusterName logicalcluster.Name, apiExport *apisv1alpha2.APIExport) error {
	if apiExport.Status.IdentityHash != "" {
		if apiExport.Status.Identity == nil {
			apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceLegacy}
		}
		conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
		return nil
	}

	switch {
	case isCoreExport(apiExport):
		apiExport.Status.IdentityHash = WellKnownIdentity(apiExport.Name)
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceWellKnown}

	case apiExport.Spec.Identity != nil && apiExport.Spec.Identity.Value != "":
		value := apiExport.Spec.Identity.Value
		if !strings.HasPrefix(value, clusterName.String()+"-") {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, identityPinInvalidReason, conditionsv1alpha1.ConditionSeverityError,
				"spec.identity.value %q must be prefixed with this export's logical cluster name %q followed by \"-\"", value, clusterName)
			return nil
		}
		holders, err := c.getAPIExportsByIdentity(value)
		if err != nil {
			return err
		}
		for _, holder := range holders {
			if logicalcluster.From(holder) != clusterName || holder.Name != apiExport.Name {
				conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, identityPinInvalidReason, conditionsv1alpha1.ConditionSeverityError,
					"spec.identity.value %q is held by APIExport %s|%s", value, logicalcluster.From(holder), holder.Name)
				return nil
			}
		}
		apiExport.Status.IdentityHash = value
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourcePinned}

	case apiExport.Spec.Identity != nil && apiExport.Spec.Identity.SecretRef != nil: //nolint:staticcheck // secretRef is honored during its deprecation window
		if err := c.updateOrVerifyIdentitySecretHash(ctx, clusterName, apiExport); err != nil {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityVerificationFailedReason, conditionsv1alpha1.ConditionSeverityError, "%v", err)
			return nil
		}
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceLegacy}
		return nil

	default:
		id, err := identity.GenerateIdentity(clusterName)
		if err != nil {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityGenerationFailedReason, conditionsv1alpha1.ConditionSeverityError, "Error generating identity: %v", err)
			return err
		}
		apiExport.Status.IdentityHash = id
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceGenerated}
	}

	conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
	return nil
}

// reconcileStorage reports, per resource with crd storage, the storage
// prefixes the export's bindings use. The prefix is the export's identity
// unless status already records another one: the one-time conversion of a
// legacy shared identity gives the export a fresh identity while its bindings
// keep serving their consumers' objects from the prefix of the formerly
// shared identity, and that prefix stays recorded here until the data is
// moved. StorageShared is True while any recorded prefix differs from the
// identity.
func (c *controller) reconcileStorage(apiExport *apisv1alpha2.APIExport) {
	if apiExport.Status.IdentityHash == "" {
		return
	}

	previous := map[apisv1alpha2.GroupResource][]string{}
	if apiExport.Status.Storage != nil {
		for _, rs := range apiExport.Status.Storage.Resources {
			previous[rs.GroupResource] = rs.Keys
		}
	}

	storage := make([]apisv1alpha2.ResourceStorageStatus, 0, len(apiExport.Spec.Resources))
	shared := false
	for _, rs := range apiExport.Spec.Resources {
		if rs.Storage.CRD == nil {
			continue
		}
		gr := apisv1alpha2.GroupResource{Group: rs.Group, Resource: rs.Name}
		keys := previous[gr]
		if len(keys) == 0 {
			keys = []string{apiExport.Status.IdentityHash}
		}
		for _, key := range keys {
			if key != apiExport.Status.IdentityHash {
				shared = true
			}
		}
		storage = append(storage, apisv1alpha2.ResourceStorageStatus{GroupResource: gr, Keys: keys})
	}

	if len(storage) > 0 {
		apiExport.Status.Storage = &apisv1alpha2.StorageStatus{Resources: storage}
	} else {
		apiExport.Status.Storage = nil
	}
	if shared {
		conditions.MarkTrue(apiExport, apisv1alpha2.StorageShared)
	} else {
		conditions.Delete(apiExport, apisv1alpha2.StorageShared)
	}
}
