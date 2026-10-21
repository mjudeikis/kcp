/*
Copyright 2022 The kcp Authors.

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
	"encoding/json"
	"fmt"
	"io"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/APIExport"

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return NewAPIExportAdmission(builtinapiexport.IsBuiltInAPI), nil
		})
}

// APIExportAdmission is an admission plugin for checking APIExport validity.
type APIExportAdmission struct {
	*admission.Handler

	isBuiltIn func(apis.GroupResource) bool

	// getAPIExportsByIdentity returns every known export (local and
	// replicated) holding the identity. nil until informers are set; the
	// held-identity check is skipped then.
	getAPIExportsByIdentity func(identityHash string) ([]*apisv1alpha2.APIExport, error)
	// getAPIIdentity returns the registry entry of an identity from the
	// replicated registry, which outlives a deleted owner: an orphaned
	// identity cannot be taken over while data may still sit under it.
	getAPIIdentity func(identityHash string) (*adminv1alpha1.APIIdentity, error)

	// decouplingEnabled reports whether the APIIdentityDecoupling feature
	// gate is on. Overridable in tests.
	decouplingEnabled func() bool
}

// NewAPIExportAdmission constructs a new APIExportAdmission admission plugin.
func NewAPIExportAdmission(isBuiltIn func(apis.GroupResource) bool) *APIExportAdmission {
	return &APIExportAdmission{
		Handler:   admission.NewHandler(admission.Create, admission.Update),
		isBuiltIn: isBuiltIn,
		decouplingEnabled: func() bool {
			return kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.APIIdentityDecoupling)
		},
	}
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&APIExportAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&APIExportAdmission{})
)

// SetKcpInformers wires the local and replicated APIExport indexers, used to
// check that a pinned identity is not held by another export.
func (e *APIExportAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	localIndexer := local.Apis().V1alpha2().APIExports().Informer().GetIndexer()
	globalIndexer := global.Apis().V1alpha2().APIExports().Informer().GetIndexer()
	indexers.AddIfNotPresentOrDie(localIndexer, cache.Indexers{indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity})
	indexers.AddIfNotPresentOrDie(globalIndexer, cache.Indexers{indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity})
	e.getAPIExportsByIdentity = func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
		return indexers.ByIndexWithFallback[*apisv1alpha2.APIExport](localIndexer, globalIndexer, indexers.APIExportByIdentity, identityHash)
	}
	identityLister := global.Admin().V1alpha1().APIIdentities().Lister()
	e.getAPIIdentity = func(identityHash string) (*adminv1alpha1.APIIdentity, error) {
		// the registry entries of every shard are replicated from their
		// system:shard clusters; the name is the identity.
		all, err := identityLister.List(labels.Everything())
		if err != nil {
			return nil, err
		}
		for _, id := range all {
			if id.Name == identityHash {
				return id, nil
			}
		}
		return nil, apierrors.NewNotFound(adminv1alpha1.Resource("apiidentities"), identityHash)
	}
}

// Validate ensures that the APIExport is valid.
func (e *APIExportAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha2.Resource("apiexports") || a.GetKind().GroupKind() != apisv1alpha2.Kind("APIExport") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	if a.GetSubresource() == "status" {
		if a.GetOperation() == admission.Update {
			return validateStatusUpdate(a, u)
		}
		return nil
	}

	switch a.GetKind().GroupVersion().Version {
	case apisv1alpha1.SchemeGroupVersion.Version:
		// v1alpha1 is deprecated, but we still need to support it for a while
		// for backward compatibility.
		// We have one non-shared validation, which checks if annotations, carrying overhanging
		// resource schemas, are set correctly.

		ae := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}

		// Before we convert to v1alpha2, we need to validate the annotations overhanging:
		if err := validateOverhangingResourceSchemas(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateOverhangingPermissionClaims(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}

		v2 := new(apisv1alpha2.APIExport)
		err := apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(ae, v2, nil)
		if err != nil {
			return fmt.Errorf("failed to convert v1alpha1 APIExport to v1alpha2: %w", err)
		}

		old, err := oldV1alpha2(a)
		if err != nil {
			return err
		}
		if err := e.validatev1alpha2(ctx, a, v2, old); err != nil {
			return err
		}
	case apisv1alpha2.SchemeGroupVersion.Version:
		// v1alpha2 is the current version.
		ae := &apisv1alpha2.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}
		old, err := oldV1alpha2(a)
		if err != nil {
			return err
		}
		if err := e.validatev1alpha2(ctx, a, ae, old); err != nil {
			return err
		}

	default:
		return admission.NewForbidden(a,
			field.Invalid(
				field.NewPath("apiVersion"),
				a.GetKind().GroupVersion().String(),
				fmt.Sprintf("unsupported API version %s", a.GetKind().GroupVersion().String())))
	}
	return nil
}

// validateStatusUpdate keeps status.identityHash immutable once set. The
// identity keys the storage of every consumer's bound instances and is what
// permission claims and wildcard clients address the export by; a status
// write that changed it would make the writer's export serve another
// export's consumers' data. Nothing but the first assignment may write it.
// The field sits at the same path in every served version.
func validateStatusUpdate(a admission.Attributes, u *unstructured.Unstructured) error {
	old, ok := a.GetOldObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetOldObject())
	}
	oldHash, _, err := unstructured.NestedString(old.Object, "status", "identityHash")
	if err != nil {
		return fmt.Errorf("failed to read status.identityHash: %w", err)
	}
	newHash, _, err := unstructured.NestedString(u.Object, "status", "identityHash")
	if err != nil {
		return fmt.Errorf("failed to read status.identityHash: %w", err)
	}
	if oldHash != "" && newHash != oldHash {
		// the one exception: the conversion of a legacy shared identity
		// (enhancements KEP 0006 §7) moves a non-owner holder onto a fresh
		// identity. It runs as kcp itself and only while the export is
		// marked IdentityLegacyShared.
		if legacySharedConversion(a, old) {
			return nil
		}
		return admission.NewForbidden(a,
			field.Forbidden(
				field.NewPath("status").Child("identityHash"),
				"identityHash is immutable once set"))
	}
	return nil
}

// legacySharedConversion reports whether a status.identityHash change is the
// legacy conversion: requested by a system:masters member on an export whose
// IdentityLegacyShared condition is True.
func legacySharedConversion(a admission.Attributes, old *unstructured.Unstructured) bool {
	if a.GetUserInfo() == nil {
		return false
	}
	masters := false
	for _, g := range a.GetUserInfo().GetGroups() {
		if g == user.SystemPrivilegedGroup {
			masters = true
			break
		}
	}
	if !masters {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(old.Object, "status", "conditions")
	for _, c := range conds {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == string(apisv1alpha2.IdentityLegacyShared) && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// oldV1alpha2 returns the previous object of an update as v1alpha2, or nil
// for a create.
func oldV1alpha2(a admission.Attributes) (*apisv1alpha2.APIExport, error) {
	if a.GetOperation() != admission.Update || a.GetOldObject() == nil {
		return nil, nil
	}
	u, ok := a.GetOldObject().(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected type %T", a.GetOldObject())
	}
	switch u.GetAPIVersion() {
	case apisv1alpha1.SchemeGroupVersion.String():
		v1 := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, v1); err != nil {
			return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}
		v2 := &apisv1alpha2.APIExport{}
		if err := apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(v1, v2, nil); err != nil {
			return nil, fmt.Errorf("failed to convert v1alpha1 APIExport to v1alpha2: %w", err)
		}
		return v2, nil
	default:
		v2 := &apisv1alpha2.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, v2); err != nil {
			return nil, fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}
		return v2, nil
	}
}

func (e *APIExportAdmission) validatev1alpha2(ctx context.Context, a admission.Attributes, ae, old *apisv1alpha2.APIExport) (err error) {
	decoupling := e.decouplingEnabled()

	for i, pc := range ae.Spec.PermissionClaims {
		if pc.IdentityHash == "" && !e.isBuiltIn(pc.GroupResource) && pc.Group != apis.GroupName {
			return admission.NewForbidden(a,
				field.Invalid(
					field.NewPath("spec").
						Child("permissionClaims").
						Index(i).
						Child("identityHash"),
					"",
					"identityHash is required for API types that are not built-in"))
		}
	}

	for i, rs := range ae.Spec.Resources {
		if err := validateResourceSchema(rs, field.NewPath("spec").Child("resources").Index(i)); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return e.validateIdentity(ctx, a, ae, old, decoupling)
}

// validateIdentity checks spec.identity: value must carry the export's own
// logical cluster name as prefix, must not be held by another export, and
// is immutable once the identity is assigned. secretRef is deprecated under
// identity decoupling and warned about.
func (e *APIExportAdmission) validateIdentity(ctx context.Context, a admission.Attributes, ae, old *apisv1alpha2.APIExport, decoupling bool) error {
	if ae.Spec.Identity == nil {
		return nil
	}
	path := field.NewPath("spec").Child("identity")

	if ae.Spec.Identity.SecretRef != nil && decoupling { //nolint:staticcheck // secretRef is honored during its deprecation window
		warning.AddWarning(ctx, "", fmt.Sprintf("%s.secretRef is deprecated: identities carry no secret; it is honored once for an export without an identity and will be removed. Use %s.value to pin an identity.", path, path))
	}

	value := ae.Spec.Identity.Value
	if value == "" {
		return nil
	}

	if old != nil && old.Status.IdentityHash != "" {
		var oldValue string
		if old.Spec.Identity != nil {
			oldValue = old.Spec.Identity.Value
		}
		if oldValue != value {
			return admission.NewForbidden(a, field.Forbidden(path.Child("value"), "value is immutable once the export has an identity"))
		}
		return nil
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err == nil && !clusterName.Empty() && !strings.HasPrefix(value, clusterName.String()+"-") {
		return admission.NewForbidden(a, field.Invalid(path.Child("value"), value,
			fmt.Sprintf("must be prefixed with this export's logical cluster name %q followed by \"-\"", clusterName)))
	}

	self := func(cluster, name string) bool {
		return name == ae.Name && (clusterName.Empty() || cluster == clusterName.String())
	}
	if e.getAPIExportsByIdentity != nil {
		holders, err := e.getAPIExportsByIdentity(value)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("failed to look up identity %q: %w", value, err))
		}
		for _, holder := range holders {
			if self(logicalcluster.From(holder).String(), holder.Name) {
				continue
			}
			return admission.NewForbidden(a, field.Invalid(path.Child("value"), value,
				fmt.Sprintf("is held by APIExport %s", logicalcluster.From(holder).Path().Join(holder.Name))))
		}
	}
	if e.getAPIIdentity != nil {
		entry, err := e.getAPIIdentity(value)
		if err != nil && !apierrors.IsNotFound(err) {
			return admission.NewForbidden(a, fmt.Errorf("failed to look up identity %q: %w", value, err))
		}
		if entry != nil {
			if owner := entry.Status.Owner; owner != nil && !self(owner.Cluster, owner.Export) {
				return admission.NewForbidden(a, field.Invalid(path.Child("value"), value,
					fmt.Sprintf("is held by APIExport %s|%s", owner.Cluster, owner.Export)))
			}
			if conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned) && (entry.Status.Owner == nil || !self(entry.Status.Owner.Cluster, entry.Status.Owner.Export)) {
				return admission.NewForbidden(a, field.Invalid(path.Child("value"), value,
					"is orphaned: its former owner is gone but data may still be stored under it"))
			}
		}
	}
	return nil
}

func validateOverhangingResourceSchemas(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(mjudeikis): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]; ok {
		// validate if we can decode overhanging resource schemas. If not, we will fail.
		var overhanging []apisv1alpha2.ResourceSchema //nolint:prealloc
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "failed to decode overhanging resource schemas")
		}

		// validate duplicates. We could have duplicates in annotations itself, or in spec + annotations.
		// We convert to v2 to check for duplicates.
		v2Schemas := make([]apisv1alpha2.ResourceSchema, len(ae.Spec.LatestResourceSchemas))
		err := apisv1alpha2.Convert_v1alpha1_LatestResourceSchema_To_v1alpha2_ResourceSchema(ae.Spec.LatestResourceSchemas, &v2Schemas)
		if err != nil {
			return field.Invalid(field.NewPath("spec").Child("latestResourceSchemas"), ae.Spec.LatestResourceSchemas, "failed to convert spec.LatestResourceSchema")
		}
		overhanging = append(overhanging, v2Schemas...)

		seen := map[string]struct{}{}
		for _, rs := range overhanging {
			if _, ok := seen[rs.Schema]; ok {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "duplicate resource schema")
			}
			seen[rs.Schema] = struct{}{}
		}
	}
	return nil
}

func validateOverhangingPermissionClaims(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(xmudrii): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]; ok {
		// validate if we can decode overhanging permission claims. If not, we will fail.
		var overhanging []apisv1alpha2.PermissionClaim
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "failed to decode overhanging permission claims")
		}

		// validate mismatches. We could have mismatches between the spec and the annotation
		// (e.g. a resource present in the annotation, but not in the spec).
		// We convert to v2 to check for mismatches.
		v2Claims := make([]apisv1alpha2.PermissionClaim, len(ae.Spec.PermissionClaims))
		for i, v1pc := range ae.Spec.PermissionClaims {
			var v2pc apisv1alpha2.PermissionClaim
			err := apisv1alpha2.Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&v1pc, &v2pc, nil)
			if err != nil {
				return field.Invalid(field.NewPath("spec").Child("permissionClaims").Index(i), ae.Spec.PermissionClaims, "failed to convert spec.PermissionClaims")
			}
			v2Claims = append(v2Claims, v2pc)
		}

		for _, o := range overhanging {
			var found bool
			for _, pc := range v2Claims {
				if pc.EqualGRI(o) {
					found = true

					break
				}
			}
			if !found {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "permission claims defined in annotation do not match permission claims defined in spec")
			}
		}
	}
	return nil
}

func validateResourceSchema(resourceSchema apisv1alpha2.ResourceSchema, path *field.Path) *field.Error {
	group := resourceSchema.Group
	if group == "" {
		group = "core"
	}

	// TODO(mjudeikis): Once v1alpha1 is removed, we can relax this if we chose to.
	// We should revisit this once we have a better understanding of the APIExport usage patterns.
	expectedSuffix := fmt.Sprintf(".%s.%s", resourceSchema.Name, group)
	if !strings.HasSuffix(resourceSchema.Schema, expectedSuffix) {
		return field.Invalid(path.Child("schema"), resourceSchema.Schema, fmt.Sprintf("must end in %s", expectedSuffix))
	}

	return nil
}
