package controller

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/types"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

type KubernetesNodeController struct {
	*baseController

	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	cacheSyncs []cache.InformerSynced
}

func NewKubernetesNodeController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	controllerID string) *KubernetesNodeController {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	knc := &KubernetesNodeController{
		baseController: newBaseController("longhorn-kubernetes-node", logger),

		controllerID: controllerID,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-kubernetes-node-controller"}),

		ds: ds,
	}

	ds.KubeNodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) { knc.enqueueNode(cur) },
		DeleteFunc: knc.enqueueNode,
	})
	knc.cacheSyncs = append(knc.cacheSyncs, ds.KubeNodeInformer.HasSynced)

	ds.NodeInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    knc.enqueueLonghornNode,
		UpdateFunc: func(old, cur interface{}) { knc.enqueueLonghornNode(cur) },
		DeleteFunc: knc.enqueueLonghornNode,
	}, 0)
	knc.cacheSyncs = append(knc.cacheSyncs, ds.NodeInformer.HasSynced)

	ds.SettingInformer.AddEventHandlerWithResyncPeriod(
		cache.FilteringResourceEventHandler{
			FilterFunc: isSettingCreateDefaultDiskLabeledNodes,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    knc.enqueueSetting,
				UpdateFunc: func(old, cur interface{}) { knc.enqueueSetting(cur) },
			},
		}, 0)
	knc.cacheSyncs = append(knc.cacheSyncs, ds.SettingInformer.HasSynced)

	return knc
}

func isSettingCreateDefaultDiskLabeledNodes(obj interface{}) bool {
	setting, ok := obj.(*longhorn.Setting)
	if !ok {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return false
		}

		// use the last known state, to enqueue, dependent objects
		setting, ok = deletedState.Obj.(*longhorn.Setting)
		if !ok {
			return false
		}
	}

	return types.SettingName(setting.Name) == types.SettingNameCreateDefaultDiskLabeledNodes
}

func (knc *KubernetesNodeController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer knc.queue.ShutDown()

	logrus.Info("Starting Longhorn Kubernetes node controller")
	defer logrus.Info("Shut down Longhorn Kubernetes node controller")

	if !cache.WaitForNamedCacheSync("longhorn kubernetes node", stopCh, knc.cacheSyncs...) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(knc.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (knc *KubernetesNodeController) worker() {
	for knc.processNextWorkItem() {
	}
}

func (knc *KubernetesNodeController) processNextWorkItem() bool {
	key, quit := knc.queue.Get()

	if quit {
		return false
	}
	defer knc.queue.Done(key)

	err := knc.syncKubernetesNode(key.(string))
	knc.handleErr(err, key)

	return true
}

func (knc *KubernetesNodeController) handleErr(err error, key interface{}) {
	if err == nil {
		knc.queue.Forget(key)
		return
	}

	log := knc.logger.WithField("KubernetesNode", key)
	if knc.queue.NumRequeues(key) < maxRetries {
		handleReconcileErrorLogging(log, err, "Failed to sync Kubernetes node")
		knc.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	handleReconcileErrorLogging(log, err, "Dropping Kubernetes node out of the queue")
	knc.queue.Forget(key)
}

func (knc *KubernetesNodeController) syncKubernetesNode(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to sync node %v", key)
	}()
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	kubeNode, err := knc.ds.GetKubernetesNodeRO(name)
	if err != nil {
		if !datastore.ErrorIsNotFound(err) {
			return err
		}
		logrus.Warnf("Kubernetes node %v has been deleted", key)
	}

	if kubeNode == nil {
		logrus.Infof("Cleaning up Longhorn node %v since failed to find the related kubernetes node", name)
		if err := knc.ds.DeleteNode(name); err != nil {
			return err
		}
		return nil
	}

	if knc.controllerID != kubeNode.Name {
		return nil
	}

	node, err := knc.ds.GetNode(kubeNode.Name)
	if err != nil {
		// cannot find the Longhorn node, may be hasn't been created yet, don't need to to sync
		return nil
	}

	existingNode := node.DeepCopy()
	defer func() {
		if err == nil && !reflect.DeepEqual(existingNode.Spec, node.Spec) {
			_, err = knc.ds.UpdateNode(node)
		}
		// requeue if it's conflict
		if apierrors.IsConflict(errors.Cause(err)) {
			logrus.Debugf("Requeue %v due to conflict: %v", key, err)
			knc.enqueueLonghornNode(node)
			err = nil
		}
	}()

	// sync default disks on labeled Nodes
	if err := knc.syncDefaultDisks(node); err != nil {
		return err
	}

	// sync node tags
	if err := knc.syncDefaultNodeTags(node); err != nil {
		return err
	}

	return nil
}

func (knc *KubernetesNodeController) enqueueSetting(obj interface{}) {
	node, err := knc.ds.GetKubernetesNodeRO(knc.controllerID)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get kubernetes node %v: %v ", knc.controllerID, err))
		return
	}
	knc.enqueueNode(node)
}

func (knc *KubernetesNodeController) enqueueLonghornNode(obj interface{}) {
	lhNode, ok := obj.(*longhorn.Node)
	if !ok {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}

		// use the last known state, to enqueue, dependent objects
		lhNode, ok = deletedState.Obj.(*longhorn.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	knc.enqueueNode(lhNode)
}

func (knc *KubernetesNodeController) enqueueNode(node interface{}) {
	key, err := controller.KeyFunc(node)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", node, err))
		return
	}

	knc.queue.Add(key)
}

// syncDefaultDisks handles creation of the customized default Disk if the setting create-default-disk-labeled-nodes is enabled.
// This allows for the default Disk to be customized and created even if the node has been labeled after initial registration with Longhorn,
// provided that there are no existing disks remaining on the node.
func (knc *KubernetesNodeController) syncDefaultDisks(node *longhorn.Node) (err error) {
	requireLabel, err := knc.ds.GetSettingAsBool(types.SettingNameCreateDefaultDiskLabeledNodes)
	if err != nil {
		return err
	}
	if !requireLabel {
		return nil
	}
	// only apply default disks if there is no existing disk
	if len(node.Spec.Disks) != 0 {
		return nil
	}
	kubeNode, err := knc.ds.GetKubernetesNodeRO(node.Name)
	if err != nil {
		return err
	}
	val, ok := kubeNode.Labels[types.NodeCreateDefaultDiskLabelKey]
	if !ok {
		return nil
	}
	val = strings.ToLower(val)

	disks := map[string]longhorn.DiskSpec{}
	switch val {
	case types.NodeCreateDefaultDiskLabelValueTrue:
		dataPath, err := knc.ds.GetSettingValueExisted(types.SettingNameDefaultDataPath)
		if err != nil {
			return err
		}
		storageReservedPercentageForDefaultDisk, err := knc.ds.GetSettingAsInt(types.SettingNameStorageReservedPercentageForDefaultDisk)
		if err != nil {
			return err
		}
		disks, err = types.CreateDefaultDisk(dataPath, storageReservedPercentageForDefaultDisk)
		if err != nil {
			return err
		}
	case types.NodeCreateDefaultDiskLabelValueConfig:
		annotation, ok := kubeNode.Annotations[types.KubeNodeDefaultDiskConfigAnnotationKey]
		if !ok {
			return nil
		}
		disks, err = types.CreateDisksFromAnnotation(annotation)
		if err != nil {
			logrus.Warnf("Failed to create disk from annotation, invalid annotation %v: %v: %v", types.KubeNodeDefaultDiskConfigAnnotationKey, val, err)
			return nil
		}
	default:
		logrus.Warnf("Got invalid label value for %v: %v", types.NodeCreateDefaultDiskLabelKey, val)
		return nil
	}

	if len(disks) == 0 {
		return nil
	}

	node.Spec.Disks = disks

	return nil
}

func (knc *KubernetesNodeController) syncDefaultNodeTags(node *longhorn.Node) error {
	if len(node.Spec.Tags) != 0 {
		return nil
	}

	kubeNode, err := knc.ds.GetKubernetesNodeRO(node.Name)
	if err != nil {
		return err
	}

	if val, exist := kubeNode.Annotations[types.KubeNodeDefaultNodeTagConfigAnnotationKey]; exist {
		tags, err := types.GetNodeTagsFromAnnotation(val)
		if err != nil {
			logrus.Warnf("Failed to set default node tags for node %v: %v", node.Name, err)
			return nil
		}
		node.Spec.Tags = tags
	}
	return nil
}
