/*
Copyright 2017 The Kubernetes Authors.

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

package main

import (
	"fmt"
	"github.com/yangyongzhi/sym-operator/pkg/apis/devops/v1"
	"github.com/yangyongzhi/sym-operator/pkg/constant"
	"github.com/yangyongzhi/sym-operator/pkg/helm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
	"time"

	clientset "github.com/yangyongzhi/sym-operator/pkg/client/clientset/versioned"
	samplescheme "github.com/yangyongzhi/sym-operator/pkg/client/clientset/versioned/scheme"
	informers "github.com/yangyongzhi/sym-operator/pkg/client/informers/externalversions/devops/v1"
	listers "github.com/yangyongzhi/sym-operator/pkg/client/listers/devops/v1"
)

const controllerAgentName = "symphony-operator"
const migrateNamespace = "default"

const (
	// SuccessSynced is used as part of the Event 'reason' when a Foo is synced
	SuccessSynced          = "Synced"
	SuccessInstalledStatus = "SuccessInstalledStatus"
	SuccessUpdatedStatus   = "SuccessUpdatedStatus"
	// ErrResourceExists is used as part of the Event 'reason' when a Foo fails
	// to sync due to a Deployment of the same name already existing.
	ResourceExists    = "ResourceExists"
	ErrGetRelease     = "ErrGetRelease"
	ErrReleaseContent = "ErrReleaseContent"
	FailInstall       = "FailInstall"
	FailUpdate        = "FailUpdate"
	ErrDeleteRelease  = "ErrDeleteRelease"

	// MessageResourceExists is the message used for Events when a resource
	// fails to sync due to a Deployment already existing
	MessageResourceExists = "Resource %q already exists and is not managed by Foo"
	// MessageResourceSynced is the message used for an Event fired when a Foo
	// is synced successfully
	MessageResourceSynced = "Migrate synced successfully"
)

// Controller is the controller implementation for Foo resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	// but symclientset is a clientset for our own API group
	kubeclientset kubernetes.Interface
	symclientset  clientset.Interface

	helmClient *helm.Client

	deploymentsLister appslisters.DeploymentLister
	deploymentsSynced cache.InformerSynced
	symLister         listers.MigrateLister
	symSynced         cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue workqueue.RateLimitingInterface

	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder record.EventRecorder
}

// NewController returns a new sample controller
func NewController(
	kubeclientset kubernetes.Interface,
	symclientset clientset.Interface, helmClient *helm.Client,
	deploymentInformer appsinformers.DeploymentInformer,
	symInformer informers.MigrateInformer) *Controller {

	// Create event broadcaster
	// Add sym-migrate-controller types to the default Kubernetes Scheme so Events can be
	// logged for sample-controller types.
	utilruntime.Must(samplescheme.AddToScheme(scheme.Scheme))
	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		kubeclientset:     kubeclientset,
		symclientset:      symclientset,
		helmClient:        helmClient,
		deploymentsLister: deploymentInformer.Lister(),
		deploymentsSynced: deploymentInformer.Informer().HasSynced,
		symLister:         symInformer.Lister(),
		symSynced:         symInformer.Informer().HasSynced,
		workqueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Sym"),
		recorder:          recorder,
	}

	klog.Info("Setting up event handlers")

	// Set up an event handler for when Foo resources change
	symInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueueMigrate,
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueMigrate(new)
		},
	})

	// Set up an event handler for when Deployment resources change. This
	// handler will lookup the owner of the given Deployment, and if it is
	// owned by a Foo resource will enqueue that Foo resource for
	// processing. This way, we don't need to implement custom logic for
	// handling Deployment resources. More info on this pattern:
	// https://github.com/kubernetes/community/blob/8cafef897a22026d42f5e5bb3f104febe7e29830/contributors/devel/controllers.md
	deploymentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.handleObject,
		UpdateFunc: func(old, new interface{}) {
			newDepl := new.(*appsv1.Deployment)
			oldDepl := old.(*appsv1.Deployment)
			if newDepl.ResourceVersion == oldDepl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployment will always have different RVs.
				return
			}
			controller.handleObject(new)
		},
		DeleteFunc: controller.handleObject,
	})

	return controller
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.Info("Starting Symphony operator...")
	// Wait for the caches to be synced before starting workers
	klog.Info("Waiting for informer caches to sync...")
	if ok := cache.WaitForCacheSync(stopCh, c.deploymentsSynced, c.symSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	klog.Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	klog.Info("Started workers")
	<-stopCh
	klog.Info("Shutting down workers")

	return nil
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key string
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(string); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			utilruntime.HandleError(fmt.Errorf("expected string in workqueue but got %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// Foo resource to be synced.
		if err := c.syncHandler(key); err != nil {
			// Put the item back on the workqueue to handle any transient errors.
			c.workqueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		klog.Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		utilruntime.HandleError(err)
		return true
	}

	return true
}

// enqueueMigrate takes a Migrate resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than Foo.
func (c *Controller) enqueueMigrate(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.workqueue.Add(key)
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the Foo resource
// with the current status of the resource.
func (c *Controller) syncHandler(key string) error {
	klog.Infof("Start sync handler method, key : '%s'", key)

	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	// Get the Foo resource with this namespace/name
	migrate, err := c.symLister.Migrates(namespace).Get(name)
	if err != nil {
		// The Foo resource may no longer exist, in which case we stop
		// processing.
		if errors.IsNotFound(err) {
			utilruntime.HandleError(fmt.Errorf("migrate '%s' in work queue no longer exists", key))
			return nil
		}

		return err
	}

	if migrate == nil {
		klog.Infof("Can not find the migrate with key: '%s'", key)
		return nil
	}
	if migrate.DeletionTimestamp != nil {
		klog.Infof("The migrate has been deleted, key: '%s'", key)
		return nil
	}

	appName := migrate.Spec.AppName
	klog.Infof("The appName of this migrate : '%s'", appName)
	if appName == "" {
		// We choose to absorb the error here as the worker would requeue the
		// resource otherwise. Instead, the next time the resource is updated
		// the resource will be queued again.
		utilruntime.HandleError(fmt.Errorf("%s: app name must be specified", key))
		return nil
	}

	/*
	 * 1.Insert the missing release
	 * 2.Delete the redundant release
	 * 3.Update the existing release
	 */
	revisions := c.reconcile(migrate)

	/* Refresh the status of migration.*/
	c.syncStatus(migrate, revisions)

	c.recorder.Event(migrate, corev1.EventTypeNormal, SuccessSynced, MessageResourceSynced)
	//+", "+strconv.Itoa(rand.Int())

	return nil
}

// handleObject will take any resource implementing metav1.Object and attempt
// to find the Foo resource that 'owns' it. It does this by looking at the
// objects metadata.ownerReferences field for an appropriate OwnerReference.
// It then enqueues that Migrate resource to be processed. If the object does not
// have an appropriate OwnerReference, it will simply be skipped.
func (c *Controller) handleObject(obj interface{}) {
	var object metav1.Object
	var ok bool
	if object, ok = obj.(metav1.Object); !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object, invalid type"))
			return
		}
		object, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("error decoding object tombstone, invalid type"))
			return
		}
		klog.Infof("Recovered deleted object '%s' from tombstone", object.GetName())
	}

	appName := object.GetLabels()[constant.AppLabel]
	klog.Infof("Processing deployment: %s, app name: %s", object.GetName(), appName)

	// Find the migrate with the app name of this deployment in this namespace.
	migrate, err := c.symLister.Migrates(object.GetNamespace()).Get(appName)
	if err != nil {
		klog.Infof("Find migrate task for deployment [%s] has an error: %s", object.GetName(), err.Error())
		return
	}
	if migrate == nil {
		klog.Infof("Can not find a migrate task for deployment: %s, ignore it.", object.GetName())
		return
	}
	if migrate.DeletionTimestamp != nil {
		klog.Infof("Perhaps the migrate which related with this deployment [%s] has been deleted, ignore it.", object.GetName())
		return
	}

	klog.Infof("##### Enqueue ##### enqueue a migrate [%s] due to a event of the deployment [%s], app name: %s", migrate.Name, object.GetName(), appName)
	c.enqueueMigrate(migrate)
	return

}

/*
 * Reconcile the releases which are running in current cluster with the releases in the migration CRD.
 */
func (c *Controller) reconcile(migrate *v1.Migrate) map[string]int32 {
	klog.Infof("##### Start to reconcile releases with migrate: '%s'", migrate.Name)
	if migrate.Status.Finished == constant.ConditionStatusTrue {
		klog.Infof("##### The status of migrate[%s] has been set as true, so no need to do anything.", migrate.Name)
		return nil
	}

	revisions := map[string]int32{}
	migrateRlses := migrate.Spec.Releases
	runningRlses, err := c.helmClient.FilterReleases(fmt.Sprintf("^%s(-gz|-rz).*(-%s|-%s)$", migrate.Spec.AppName, constant.BlueGroup, constant.GreenGroup))
	if err != nil {
		c.recorder.Event(migrate, corev1.EventTypeWarning, ErrDeleteRelease,
			fmt.Sprintf("Can not find any running releases when you want to update [%s], error : %s", migrate.Name, err.Error()))
		return nil
	}

	// At first, uninstall the un-defined release in the newest migration.
	for _, runningRls := range runningRlses {
		var foundDefinition = false
		for _, migrateRls := range migrateRlses {
			if runningRls.Name == migrateRls.Name {
				foundDefinition = true
			}
		}

		if !foundDefinition {
			klog.Infof("##### The running release [%s] has not been defind in migration, we should delete it.", runningRls.Name)
			_, err := c.helmClient.UninstallRelease(runningRls.Name)
			if err != nil {
				c.recorder.Event(migrate, corev1.EventTypeWarning, ErrDeleteRelease,
					fmt.Sprintf("##### Delete release [%s] has an error : %s", runningRls.Name, err.Error()))
				continue
			} else {
				// Don't save the version of the deleted release into the status.
				c.recorder.Event(migrate, corev1.EventTypeNormal, SuccessSynced,
					fmt.Sprintf("##### Release [%s] has been deleted successfully.", runningRls.Name))
			}

			return revisions
		}
	}

	// Secondly, update the release with the newest releases in the current migration.
	for _, migrateRls := range migrateRlses {
		var rlsIsExist = false
		for _, runningRls := range runningRlses {
			if migrateRls.Name == runningRls.Name {
				rlsIsExist = true
				klog.Infof("##### We already found the running release [%s] with current migration, then we will update it immediately.", migrateRls.Name)
				if migrate.Status.ReleaseRevision[migrateRls.Name] == runningRls.Version {
					klog.Infof("##### Version of release in the status [%s] has been updated to version [%d], no need to do anything.",
						migrateRls.Name, migrate.Status.ReleaseRevision[migrateRls.Name])
					break
				}

				// The version is not same as the one has been aved in status.
				updateResponse, err := c.helmClient.UpdateRelease(migrateRls.Name, migrate.Spec.Chart, migrateRls.Raw)
				if err != nil {
					c.recorder.Event(migrate, corev1.EventTypeWarning, ErrReleaseContent,
						fmt.Sprintf("Update release [%s] has an error : %s", migrateRls.Name, err))
				} else {
					revisions[migrateRls.Name] = updateResponse.Release.Version
					c.recorder.Event(migrate, corev1.EventTypeNormal, SuccessUpdatedStatus,
						fmt.Sprintf("Update release [%s] successfully, version : %d", migrateRls.Name, updateResponse.Release.Version))
				}

				return revisions
			}
		}

		// If the release you want to update has not been exist, we install it first.
		if !rlsIsExist {
			klog.Infof("##### Can not find release [%s] when you want to update it, so you should use installing instead of updating.", migrateRls.Name)
			installResponse, err := c.helmClient.InstallRelease(migrateRls.Namespace, migrateRls.Name, migrate.Spec.Chart, migrateRls.Raw)
			if err != nil {
				c.recorder.Event(migrate, corev1.EventTypeWarning, ErrReleaseContent,
					fmt.Sprintf("Install release [%s] has an error : %s", migrateRls.Name, err))
			} else {
				revisions[migrateRls.Name] = installResponse.Release.Version
				c.recorder.Event(migrate, corev1.EventTypeNormal, SuccessInstalledStatus,
					fmt.Sprintf("Install release [%s] successfully, version : %d", migrateRls.Name, installResponse.Release.Version))
			}
			return revisions
		}
	}

	return revisions
}

// Synchronize the status of migrate which has been set as a installing one
func (c *Controller) syncStatus(migrate *v1.Migrate, revisions map[string]int32) error {
	migrateCopy := migrate.DeepCopy()
	initialFinished := migrateCopy.Status.Finished
	now := metav1.Now()

	// Find all deployment with the app name, always there should be two deployments with this app name (blue & green).
	//deployment, err := c.deploymentsLister.Deployments(object.GetNamespace()).Get(object.GetName())
	//r, _ := labels.NewRequirement("app", selection.Equals, []string{appName})
	labelSet := labels.Set{}
	labelSet[constant.AppLabel] = migrateCopy.Spec.AppName
	deployments, err := c.deploymentsLister.Deployments(migrate.GetNamespace()).List(labels.SelectorFromSet(labelSet))
	// If an error occurs during Get/Create, we'll requeue the item so we can
	// attempt processing again later. This could have been caused by a
	// temporary network failure, or any other transient reason.
	if err != nil {
		klog.Infof("Can not find the deployments for migrate [%s], ignore it.", migrate.GetName())
		return nil
	}
	if deployments != nil && len(deployments) > 0 {
		klog.Infof("===== The deployments for migrate [%s] is not null or not empty, then update the status of the current migrate.", migrate.GetName())
		for _, deploy := range deployments {
			var message = ""
			rlsName := deploy.Spec.Template.Labels[constant.ReleaseLabel]
			conditionType := constant.ConcatConditionType(rlsName)

			var currentRelease *v1.ReleasesConfig
			for _, rls := range migrate.Spec.Releases {
				if rls.Name == rlsName {
					currentRelease = rls
				}
			}

			if currentRelease == nil {
				klog.Infof("===== Can not find release in Spec part with deployment's label %s, so wait for it to disappear.", rlsName)
				continue
			}

			message = fmt.Sprintf("Deployment [%s]'s status: desired replica:%d, available:%d, Migrate replica count:%d",
				deploy.GetName(), deploy.Status.Replicas, deploy.Status.AvailableReplicas, currentRelease.Replicas)
			upsertCondition(migrateCopy, v1.MigrateCondition{
				conditionType, constant.ConditionStatusFalse, now, now, "", message})
			klog.Info("===== " + message)
			if deploy.Status.Replicas == deploy.Status.AvailableReplicas && deploy.Status.AvailableReplicas == currentRelease.Replicas {
				getRelease, err := c.helmClient.GetRelease(currentRelease.Name)
				if err != nil {
					klog.Infof("Find release [%s] has an error : %s", rlsName, err.Error())
					c.recorder.Event(migrate, corev1.EventTypeWarning, ErrGetRelease,
						fmt.Sprintf("Error - Get the release [%s] info : %s", rlsName, err))
				} else {
					if migrateCopy.Status.ReleaseRevision == nil {
						message = fmt.Sprintf("The revision information in Status is null, maybe you don't update the release yet. migrate [%s]",
							migrateCopy.Name)
						klog.Info("===== " + message)
						upsertCondition(migrateCopy, v1.MigrateCondition{conditionType, constant.ConditionStatusFalse, now, now, "", message})
						continue
					}

					if getRelease != nil && getRelease.Version == migrateCopy.Status.ReleaseRevision[currentRelease.Name] {
						upsertCondition(migrateCopy,
							v1.MigrateCondition{conditionType, constant.ConditionStatusTrue, now, now, "", message})
					} else {
						message = fmt.Sprintf("The revision information  [%d] in Status is not equals to the revision  [%d] in helm, wait for the next updating.",
							migrateCopy.Name)
						klog.Info("===== " + message)
						upsertCondition(migrateCopy, v1.MigrateCondition{
							conditionType, constant.ConditionStatusFalse, now, now, "", message})
					}
				}
			} else {
				message = fmt.Sprintf("Waiting for the deployment [%s] is available if you want to update the Status of migrate [%s]",
					migrateCopy.Name, deploy.Name)
				klog.Info("===== " + message)
			}
		}
	}

	calFinalStatus(migrateCopy, deployments)
	if initialFinished == constant.ConditionStatusFalse || migrateCopy.Status.Finished == constant.ConditionStatusFalse {
		migrateCopy.Status.LastUpdateTime = &now
	}

	if revisions != nil && len(revisions) > 0 {
		for key, value := range revisions {
			if migrateCopy.Status.ReleaseRevision == nil {
				migrateCopy.Status.ReleaseRevision = map[string]int32{}
			}

			migrateCopy.Status.ReleaseRevision[key] = value
		}
	}

	_, err = c.symclientset.DevopsV1().Migrates(migrate.Namespace).Update(migrateCopy)

	return err
}

// Updating or inserting a condition for this migrate
func upsertCondition(migrateCopy *v1.Migrate, condition v1.MigrateCondition) {
	if len(migrateCopy.Status.Conditions) <= 0 {
		migrateCopy.Status.Conditions = append(migrateCopy.Status.Conditions, condition)
		return
	}

	for i, _ := range migrateCopy.Status.Conditions {
		if migrateCopy.Status.Conditions[i].Type == condition.Type {
			migrateCopy.Status.Conditions[i] = condition
			//c.LastProbeTime = condition.LastProbeTime
			//c.LastTransitionTime = condition.LastTransitionTime
			//c.Message = condition.Message
			//c.Reason = condition.Reason
			return
		}
	}

	migrateCopy.Status.Conditions = append(migrateCopy.Status.Conditions, condition)
}

// You should calculate the final status for this migrate after inserting (update) its conditions.
func calFinalStatus(migrateCopy *v1.Migrate, deployments []*appsv1.Deployment) {
	if len(migrateCopy.Status.Conditions) != len(migrateCopy.Spec.Releases) {
		migrateCopy.Status.Finished = constant.ConditionStatusFalse
		return
	}

	for _, c := range migrateCopy.Status.Conditions {
		if c.Status == constant.ConditionStatusFalse {
			migrateCopy.Status.Finished = constant.ConditionStatusFalse
			return
		}
	}

	if len(deployments) != len(migrateCopy.Spec.Releases) {
		migrateCopy.Status.Finished = constant.ConditionStatusFalse
		return
	}

	migrateCopy.Status.Finished = constant.ConditionStatusTrue
}

//It is always used for a test case if you want to delete all the redundant conditions.
func clearConditions(migrateCopy *v1.Migrate) {
	if len(migrateCopy.Status.Conditions) <= 0 {
		return
	}

	migrateCopy.Status.Conditions = make([]v1.MigrateCondition, 0)

}
