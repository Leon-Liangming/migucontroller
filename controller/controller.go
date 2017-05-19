package controller

import (
	"sync"
	"reflect"
	"strings"

	"migucontroller/controller/taskqueue"
	"github.com/golang/glog"

	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/client/record"
	unversionedcore "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset/typed/core/internalversion"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/watch"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/util/flowcontrol"
	"k8s.io/kubernetes/pkg/selection"
	"k8s.io/kubernetes/pkg/apis/extensions"

	"time"
	"fmt"
	"os"
	"bytes"
	"io/ioutil"
	"os/exec"
)

const (
	podStoreSyncedPollPeriod = 1 * time.Second
)
func ingressListFunc(c *clientset.Clientset, ns string) func(api.ListOptions) (runtime.Object, error) {
	return func(opts api.ListOptions) (runtime.Object, error) {
		return c.Extensions().Ingresses(ns).List(opts)
	}
}

func ingressWatchFunc(c *clientset.Clientset, ns string) func(options api.ListOptions) (watch.Interface, error) {
	return func(options api.ListOptions) (watch.Interface, error) {
		return c.Extensions().Ingresses(ns).Watch(options)
	}
}

func serviceListFunc(c *clientset.Clientset, ns string) func(api.ListOptions) (runtime.Object, error) {
	return func(opts api.ListOptions) (runtime.Object, error) {
		return c.Services(ns).List(opts)
	}
}

func serviceWatchFunc(c *clientset.Clientset, ns string) func(options api.ListOptions) (watch.Interface, error) {
	return func(options api.ListOptions) (watch.Interface, error) {
		return c.Services(ns).Watch(options)
	}
}

func endpointsListFunc(c *clientset.Clientset, ns string) func(api.ListOptions) (runtime.Object, error) {
	return func(opts api.ListOptions) (runtime.Object, error) {
		return c.Endpoints(ns).List(opts)
	}
}

func endpointsWatchFunc(c *clientset.Clientset, ns string) func(options api.ListOptions) (watch.Interface, error) {
	return func(options api.ListOptions) (watch.Interface, error) {
		return c.Endpoints(ns).Watch(options)
	}
}

// StoreToIngressLister makes a Store that lists Ingress.
type StoreToIngressLister struct {
	cache.Store
}

type Controller struct {
	client *clientset.Clientset
	svcSelector labels.Selector

	ingController  *cache.Controller
	endpController *cache.Controller
	svcController  *cache.Controller

	ingLister  StoreToIngressLister
	endpLister cache.StoreToEndpointsLister
	svcLister  cache.StoreToServiceLister

	recorder record.EventRecorder
	syncQueue *taskqueue.TaskQueue

	reloadRateLimiter flowcontrol.RateLimiter
	reloadLock *sync.Mutex

	// template loaded ready to be used to generate the nginx configuration file
	template *Template
	ConfigFile string
	TempPath   string
	// stopLock is used to enforce only a single call to Stop is active.
	// Needed because we allow stopping through an http endpoint and
	// allowing concurrent stoppers leads to stack traces.
	stopLock sync.Mutex
	shutdown bool
	stopCh   chan struct{}
}

func NewController(kubeClient *clientset.Clientset,  serviceLabels, namespace string, resyncPeriod time.Duration, nginxTemplate string, configPath string) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&unversionedcore.EventSinkImpl{Interface: kubeClient.Core().Events(namespace)})

	svcSelector,err :=  parserServiceLabels(serviceLabels)
	if err != nil {
		glog.Errorf("Parser service labels failed. %v", err)
		return nil
	}

	controller := &Controller{
		client:			kubeClient,
		stopCh:         	make(chan struct{}),
		recorder: 		eventBroadcaster.NewRecorder(api.EventSource{
			Component: "nginx-ingress-controller",
		}),
		svcSelector: 		svcSelector,
		reloadRateLimiter: 	flowcontrol.NewTokenBucketRateLimiter(0.1, 1),
		reloadLock:        	&sync.Mutex{},
		TempPath:		nginxTemplate,
		ConfigFile:		configPath,
	}

	controller.syncQueue = taskqueue.NewTaskQueue(controller.Sync)

	eventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			controller.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			controller.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				controller.syncQueue.Enqueue(cur)
			}
		},
	}

	ingEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addIng := obj.(*extensions.Ingress)

			controller.recorder.Eventf(addIng, api.EventTypeNormal, "CREATE", fmt.Sprintf("%s/%s", addIng.Namespace, addIng.Name))
			controller.syncQueue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			delIng := obj.(*extensions.Ingress)

			controller.recorder.Eventf(delIng, api.EventTypeNormal, "DELETE", fmt.Sprintf("%s/%s", delIng.Namespace, delIng.Name))
			controller.syncQueue.Enqueue(obj)
		},
		UpdateFunc: func(old, cur interface{}) {

			if !reflect.DeepEqual(old, cur) {
				upIng := cur.(*extensions.Ingress)
				controller.recorder.Eventf(upIng, api.EventTypeNormal, "UPDATE", fmt.Sprintf("%s/%s", upIng.Namespace, upIng.Name))
				controller.syncQueue.Enqueue(cur)
			}
		},
	}

	controller.ingLister.Store, controller.ingController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc:  ingressListFunc(controller.client, namespace),
			WatchFunc: ingressWatchFunc(controller.client, namespace),
		},
		&extensions.Ingress{}, resyncPeriod, ingEventHandler)


	controller.endpLister.Store, controller.endpController = cache.NewInformer(
		&cache.ListWatch{
			ListFunc:  endpointsListFunc(controller.client, namespace),
			WatchFunc: endpointsWatchFunc(controller.client, namespace),
		},
		&api.Endpoints{}, resyncPeriod, eventHandler)

	controller.svcLister.Indexer, controller.svcController = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc:  serviceListFunc(controller.client, namespace),
			WatchFunc: serviceWatchFunc(controller.client, namespace),
		},
		&api.Service{}, resyncPeriod, cache.ResourceEventHandlerFuncs{},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	var onChange func()
	onChange = func() {
		template, err := NewTemplate(controller.TempPath, onChange)
		if err != nil {
			glog.Warningf("invalid NGINX template: %v", err)
			return
		}

		controller.template = template
		glog.Info("new NGINX template loaded")
	}

	template, err := NewTemplate(controller.TempPath, onChange)
	if err != nil {
		glog.Fatalf("invalid NGINX template: %v", err)
	}

	controller.template = template

	return controller
}

func (controller *Controller) Sync(key string) error {
	if !controller.ControllersInSync() {
		time.Sleep(podStoreSyncedPollPeriod)
		return fmt.Errorf("deferring sync till endpoints controller has synced")
	}

	controller.reloadRateLimiter.Accept()
	controller.reloadLock.Lock()
	defer controller.reloadLock.Unlock()

	glog.Infof("Sync nginx config file...")
	//svcList, err := controller.getAllSpecifyServices()
	//if err != nil {
	//	glog.Errorf("get all services failed. %v", err)
	//	return err
	//}
	ings := controller.ingLister.Store.List()

	upstreams := controller.TransferToUpstream(ings)
	glog.Infof("Success get upsteams %v", len(upstreams))

	newCfg, err := controller.template.Write(upstreams)
	if err != nil {
		return fmt.Errorf("failed to write new nginx configuration. Avoiding reload: %v", err)
	}

	changed, err := controller.needsReload(newCfg)
	if err != nil {
		glog.Errorf("reload nginx config file failed. %v", err)
		// 清除配置文件，保证下次能够重新reload
		os.Rename(controller.ConfigFile, controller.ConfigFile + "-failed")
		return err
	}

	if changed {
		if err := controller.shellOut("nginx -s reload"); err == nil {
			glog.Info("change in configuration detected. Reloading...")
		} else {
			glog.Errorf("nginx reload failed. %v", err)
			// 清除配置文件，保证下次能够重新reload
			os.Rename(controller.ConfigFile, controller.ConfigFile + "-failed")
			return err
		}
	}
	return nil
}

// Run starts the loadbalancer controller.
func (controller *Controller) Run() {
	glog.Infof("Starting MIGU Controller")

	go controller.endpController.Run(controller.stopCh)
	go controller.svcController.Run(controller.stopCh)
	go controller.ingController.Run(controller.stopCh)

	go controller.syncQueue.Run(time.Second, controller.stopCh)

	<-controller.stopCh
}

// Stop stops the loadbalancer controller.
func (controller *Controller) Stop() error {
	// Stop is invoked from the http endpoint.
	controller.stopLock.Lock()
	defer controller.stopLock.Unlock()

	// Only try draining the workqueue if we haven't already.
	if !controller.shutdown {
		controller.shutdown = true
		close(controller.stopCh)


		glog.Infof("Shutting down controller queues.")
		controller.syncQueue.Shutdown()

		return nil
	}

	return fmt.Errorf("shutdown already in progress")
}

func (controller *Controller) ControllersInSync() bool {
	return controller.svcController.HasSynced() &&
		controller.endpController.HasSynced()
}

func parserServiceLabels(serviceLabels string) (labels.Selector, error) {
	serviceLabels = strings.Replace(serviceLabels, " ", "", -1)
	singleLabels := strings.Split(serviceLabels, ",")

	selector := labels.NewSelector()
	for _, singleLabel := range singleLabels {
		data := strings.Split(singleLabel, "=")
		key := data[0]
		value := []string{}
		value = append(value, data[1])
		req, err := labels.NewRequirement(key, selection.Equals, value)
		if err != nil {
			glog.Errorf("Set label failed, %v, %v", singleLabel, err)
			continue
		}

		selector = selector.Add(*req)
	}

	glog.Infof("Get Service Labels's Requirement Success, %v", selector.String())
	return selector, nil
}

func (controller *Controller) getAllSpecifyServices() ([]*api.Service, error) {

	svcList, err := controller.svcLister.List(controller.svcSelector)
	if err != nil {
		glog.Errorf("Get %v services failed.", controller.svcSelector.String())
		return svcList, err
	}

	return svcList, nil
}

func (controller *Controller) needsReload(data []byte) (bool, error) {
	filename := controller.ConfigFile
	_, err := os.Stat(filename)
	//if err != nil {
	//	glog.Errorf("file stat failed. %v", err)
	//	return false, err
	//}
	var in *os.File
	if os.IsNotExist(err) {
		in, err = os.Create(filename)
	} else {
		in, err = os.Open(filename)
	}

	src, err := ioutil.ReadAll(in)
	in.Close()
	if err != nil {
		glog.Errorf("read file failed. %v", err)
		return false, err
	}

	if !bytes.Equal(src, data) {
		err = ioutil.WriteFile(filename, data, 0644)
		if err != nil {
			glog.Errorf("write file faild. %v", err)
			return false, err
		}

		dData, err := diff(src, data)
		if err != nil {
			glog.Errorf("error computing diff: %s", err)
			return true, nil
		}

		if glog.V(2) {
			glog.Infof("NGINX configuration diff a/%s b/%s\n", filename, filename)
			glog.Infof("%v", string(dData))
		}

		return len(dData) > 0, nil
	}

	return false, nil
}

func diff(b1, b2 []byte) (data []byte, err error) {
	f1, err := ioutil.TempFile("", "")
	if err != nil {
		return
	}
	defer os.Remove(f1.Name())
	defer f1.Close()

	f2, err := ioutil.TempFile("", "")
	if err != nil {
		return
	}
	defer os.Remove(f2.Name())
	defer f2.Close()

	f1.Write(b1)
	f2.Write(b2)

	data, err = exec.Command("diff", "-u", f1.Name(), f2.Name()).CombinedOutput()
	if len(data) > 0 {
		err = nil
	}
	return
}

// shellOut executes a command and returns its combined standard output and standard
// error in case of an error in the execution
func (controller *Controller) shellOut(cmd string) error {
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		glog.Errorf("failed to execute %v: %v", cmd, string(out))
		return err
	}

	return nil
}