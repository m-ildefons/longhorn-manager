package objectstore

import (
	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/webhook/admission"
	"github.com/pkg/errors"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	werror "github.com/longhorn/longhorn-manager/webhook/error"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
)

type objectStoreValidator struct {
	admission.DefaultValidator
	ds *datastore.DataStore
}

func NewValidator(ds *datastore.DataStore) admission.Validator {
	return &objectStoreValidator{ds: ds}
}

func (osv *objectStoreValidator) Resource() admission.Resource {
	return admission.Resource{
		Name:       "objectstore",
		Scope:      admissionregv1.NamespacedScope,
		APIGroup:   longhorn.SchemeGroupVersion.Group,
		APIVersion: longhorn.SchemeGroupVersion.Version,
		ObjectType: &longhorn.ObjectStore{},
		OperationTypes: []admissionregv1.OperationType{
			admissionregv1.Create,
			admissionregv1.Update,
		},
	}
}

func (osv *objectStoreValidator) Crete(req *admission.Request, newObj runtime.Object) (err error) {
	store := newObj.(*longhorn.ObjectStore)

	if err = osv.validateNamespace(store); err != nil {
		return err
	}

	if err = osv.validateCredentialsNamespace(store); err != nil {
		return err
	}

	minSize := resource.MustParse("1Gi")
	if store.Spec.Size.Cmp(minSize) < 0 {
		return werror.NewInvalidError("object store must be at least 1Gi in size", "spec.storage.size")
	}

	return nil
}

func (osv *objectStoreValidator) Update(req *admission.Request, oldObj, newObj runtime.Object) (err error) {
	oldStore := oldObj.(*longhorn.ObjectStore)
	newStore := newObj.(*longhorn.ObjectStore)

	if err = osv.validateNamespace(newStore); err != nil {
		return err
	}

	if oldStore.Spec.Size.Cmp(newStore.Spec.Size) > 0 {
		return werror.NewInvalidError("can not shrink store size", "spec.storage.size")
	}

	if oldStore.Spec.VolumeParameters.NumberOfReplicas != newStore.Spec.VolumeParameters.NumberOfReplicas {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.numberOfReplicas")
	}

	if oldStore.Spec.VolumeParameters.ReplicaSoftAntiAffinity != newStore.Spec.VolumeParameters.ReplicaSoftAntiAffinity {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.replicaSoftAntiAffinity")
	}

	if oldStore.Spec.VolumeParameters.ReplicaZoneSoftAntiAffinity != newStore.Spec.VolumeParameters.ReplicaZoneSoftAntiAffinity {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.replicaZoneSoftAntiAffinity")
	}

	if oldStore.Spec.VolumeParameters.ReplicaDiskSoftAntiAffinity != newStore.Spec.VolumeParameters.ReplicaDiskSoftAntiAffinity {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.replicaDiskSoftAntiAffinity")
	}

	if oldStore.Spec.VolumeParameters.DataLocality != newStore.Spec.VolumeParameters.DataLocality {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.dataLocality")
	}

	if oldStore.Spec.VolumeParameters.FromBackup != newStore.Spec.VolumeParameters.FromBackup {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.fromBackup")
	}

	if oldStore.Spec.VolumeParameters.StaleReplicaTimeout != newStore.Spec.VolumeParameters.StaleReplicaTimeout {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.staleReplicaTimeout")
	}

	if oldStore.Spec.VolumeParameters.ReplicaAutoBalance != newStore.Spec.VolumeParameters.ReplicaAutoBalance {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.replicaAutoBalance")
	}

	if oldStore.Spec.VolumeParameters.RevisionCounterDisabled != newStore.Spec.VolumeParameters.RevisionCounterDisabled {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.revisionCounterDisabled")
	}

	if oldStore.Spec.VolumeParameters.UnmapMarkSnapChainRemoved != newStore.Spec.VolumeParameters.UnmapMarkSnapChainRemoved {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.unmapMarkSnapChainRemoved")
	}

	if oldStore.Spec.VolumeParameters.BackendStoreDriver != newStore.Spec.VolumeParameters.BackendStoreDriver {
		return werror.NewInvalidError("immutable property mutated", "spec.storage.backendStorageDriver")
	}

	return nil
}

func (osv *objectStoreValidator) validateNamespace(objectstore *longhorn.ObjectStore) error {
	namespace, err := osv.ds.GetLonghornNamespace()
	if err != nil {
		return errors.Wrapf(err, "api error while trying to read longhorn namespace object")
	}

	if objectstore.ObjectMeta.Namespace != namespace.Name {
		return werror.NewInvalidError("metadata.namespace is invalid", "metadata.namespace")
	}
	return nil
}

func (osv *objectStoreValidator) validateCredentialsNamespace(objectstore *longhorn.ObjectStore) error {
	if objectstore.ObjectMeta.Namespace != objectstore.Spec.Credentials.Namespace {
		return werror.NewInvalidError("spec.credentials.namespace is invalid", "spec.cerentials.namespace")
	}

	return nil
}
