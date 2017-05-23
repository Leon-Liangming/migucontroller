package controller

import (
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/labels"
	podutil "k8s.io/kubernetes/pkg/api/pod"
	"k8s.io/kubernetes/pkg/apis/extensions"

	"strconv"
	"fmt"
	"github.com/golang/glog"

	"reflect"
	"encoding/json"
	"sort"
)

type Upstream struct {
	UpstreamName string
	Endpoints    []EndpointInfo
}


// ServerByName sorts server by name
type UpstreamByName []Upstream

func (c UpstreamByName) Len() int      { return len(c) }
func (c UpstreamByName) Swap(i, j int) { c[i], c[j] = c[j], c[i] }
func (c UpstreamByName) Less(i, j int) bool {
	return c[i].UpstreamName < c[j].UpstreamName
}

type EndpointInfo struct {
	Address string
	Port    int32
}

func (controller *Controller) TransferToUpstream(ings []interface{}) []Upstream {
	// upstreams := []Upstream{}
	glog.V(3).Infof("Get %v ings data.", len(ings))
	upstreams := make(map[string]*Upstream)
	for _, ingIf := range ings {
		ing := ingIf.(*extensions.Ingress)

		for _, rule := range ing.Spec.Rules {
			if rule.IngressRuleValue.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				name := fmt.Sprintf("%v_%v_%v", path.Backend.ServiceName, ing.GetNamespace(), path.Backend.ServicePort.String())
				if _, ok := upstreams[name]; ok {
					continue
				}
				svcKey := fmt.Sprintf("%v/%v", ing.GetNamespace(), path.Backend.ServiceName)
				eps, err := controller.getSvcEndpoints(svcKey, path.Backend.ServicePort.String())
				if err != nil {
					glog.Errorf("get service %v' pod failed. %v", svcKey, err)
					continue
				}
				if len(eps) == 0 {
					glog.Warningf("service %v does not have any active endpoints", svcKey)
					continue
				}
				upstreams[name] = controller.NewUpstream(name)
				upstreams[name].Endpoints = eps
			}
		}
	}

	var upstreamArray []Upstream
	for _, up := range upstreams {
		upstreamArray = append(upstreamArray, *up)
	}

	//排序
	sort.Sort(UpstreamByName(upstreamArray))
	return upstreamArray
}

func (controller *Controller) getSvcEndpoints(svcKey, backendPort string) ([]EndpointInfo, error) {
	var eps []EndpointInfo
	svcObj, svcExists, err := controller.svcLister.Indexer.GetByKey(svcKey)
	if err != nil {
		return eps, fmt.Errorf("error getting service %v from the cache: %v", svcKey, err)
	}

	if !svcExists {
		return eps, fmt.Errorf("service %v does not exists", svcKey)
	}

	svc := svcObj.(*api.Service)
	glog.V(3).Infof("service detail %v, %v", svc, svcKey)
	for _, servicePort := range svc.Spec.Ports {

		glog.V(3).Infof("service Ports %v, %v", servicePort, backendPort)
		// targetPort could be a string, use the name or the port (int)
		if strconv.Itoa(int(servicePort.Port)) == backendPort ||
			servicePort.TargetPort.String() == backendPort ||
			strconv.Itoa(int(servicePort.NodePort)) == backendPort ||
			servicePort.Name == backendPort {

			endps, err := controller.getEndpoints(svc, servicePort.TargetPort, api.ProtocolTCP)
			if err != nil {
				glog.Errorf("get service %v's %v endpoint failed. %v", svc, servicePort.TargetPort.String(), err)
				continue
			}
			if len(endps) == 0 {
				glog.Warningf("service %v does not have any active endpoints", svcKey)
				continue
			}

			eps = append(eps, endps...)
			break
		}
	}

	return eps, nil
}
func (controller *Controller) NewUpstream(name string) *Upstream {
	return &Upstream{
		UpstreamName:     name,
		Endpoints: []EndpointInfo{},
	}
}

func  (controller *Controller) getEndpoints (
	s *api.Service,
	servicePort intstr.IntOrString,
	proto api.Protocol,
) ([]EndpointInfo, error) {
	var eps []EndpointInfo
	ep, err := controller.endpLister.GetServiceEndpoints(s)
	if err != nil {
		glog.Warningf("unexpected error obtaining service endpoints: %v", err)
		return eps, err
	}

	for _, ss := range ep.Subsets {
		for _, epPort := range ss.Ports {

			if !reflect.DeepEqual(epPort.Protocol, proto) {
				continue
			}

			var targetPort int32
			switch servicePort.Type {
			case intstr.Int:
				if int(epPort.Port) == servicePort.IntValue() {
					targetPort = epPort.Port
				}
			case intstr.String:
				namedPorts := s.ObjectMeta.Annotations
				val, ok := namedPortMapping(namedPorts).getPort(servicePort.StrVal)
				if ok {
					port, err := strconv.Atoi(val)
					if err != nil {
						glog.Warningf("%v is not valid as a port", val)
						continue
					}

					targetPort = int32(port)
				} else {
					newnp, err := controller.checkSvcForUpdate(s)
					if err != nil {
						glog.Warningf("error mapping service ports: %v", err)
						continue
					}
					val, ok := namedPortMapping(newnp).getPort(servicePort.StrVal)
					if ok {
						port, err := strconv.Atoi(val)
						if err != nil {
							glog.Warningf("%v is not valid as a port", val)
							continue
						}

						targetPort = int32(port)
					}
				}
			}

			if targetPort == 0 {
				continue
			}

			for _, epAddress := range ss.Addresses {
				var ep EndpointInfo
				ep.Address = epAddress.IP
				ep.Port = targetPort
				eps = append(eps, ep)
			}

		}
	}
	return eps, nil
}

// checkSvcForUpdate verifies if one of the running pods for a service contains
// named port. If the annotation in the service does not exists or is not equals
// to the port mapping obtained from the pod the service must be updated to reflect
// the current state
func (controller *Controller) checkSvcForUpdate(svc *api.Service) (map[string]string, error) {
	// get the pods associated with the service
	// TODO: switch this to a watch
	pods, err := controller.client.Pods(svc.Namespace).List(api.ListOptions{
		LabelSelector: labels.Set(svc.Spec.Selector).AsSelector(),
	})

	namedPorts := map[string]string{}
	if err != nil {
		return namedPorts, fmt.Errorf("error searching service pods %v/%v: %v", svc.Namespace, svc.Name, err)
	}

	if len(pods.Items) == 0 {
		return namedPorts, nil
	}

	// we need to check only one pod searching for named ports
	pod := &pods.Items[0]
	glog.V(4).Infof("checking pod %v/%v for named port information", pod.Namespace, pod.Name)
	for i := range svc.Spec.Ports {
		servicePort := &svc.Spec.Ports[i]

		_, err := strconv.Atoi(servicePort.TargetPort.StrVal)
		if err != nil {
			portNum, err := podutil.FindPort(pod, servicePort)
			if err != nil {
				glog.V(4).Infof("failed to find port for service %s/%s: %v", svc.Namespace, svc.Name, err)
				continue
			}

			if servicePort.TargetPort.StrVal == "" {
				continue
			}

			namedPorts[servicePort.TargetPort.StrVal] = fmt.Sprintf("%v", portNum)
		}
	}

	if svc.ObjectMeta.Annotations == nil {
		svc.ObjectMeta.Annotations = map[string]string{}
	}

	curNamedPort := svc.ObjectMeta.Annotations[namedPortAnnotation]
	if len(namedPorts) > 0 && !reflect.DeepEqual(curNamedPort, namedPorts) {
		data, _ := json.Marshal(namedPorts)

		newSvc, err := controller.client.Services(svc.Namespace).Get(svc.Name)
		if err != nil {
			return namedPorts, fmt.Errorf("error getting service %v/%v: %v", svc.Namespace, svc.Name, err)
		}

		if newSvc.ObjectMeta.Annotations == nil {
			newSvc.ObjectMeta.Annotations = map[string]string{}
		}

		newSvc.ObjectMeta.Annotations[namedPortAnnotation] = string(data)
		glog.Infof("updating service %v with new named port mappings", svc.Name)
		_, err = controller.client.Services(svc.Namespace).Update(newSvc)
		if err != nil {
			return namedPorts, fmt.Errorf("error syncing service %v/%v: %v", svc.Namespace, svc.Name, err)
		}

		return newSvc.ObjectMeta.Annotations, nil
	}

	return namedPorts, nil
}

var namedPortAnnotation      = "ingress.kubernetes.io/named-ports"
type namedPortMapping map[string]string

// getPort returns the port defined in a named port
func (npm namedPortMapping) getPort(name string) (string, bool) {
	val, ok := npm.getPortMappings()[name]
	return val, ok
}

// getPortMappings returns the map containing the
// mapping of named port names and the port number
func (npm namedPortMapping) getPortMappings() map[string]string {
	data := npm[namedPortAnnotation]
	var mapping map[string]string
	if data == "" {
		return mapping
	}
	if err := json.Unmarshal([]byte(data), &mapping); err != nil {
		glog.Errorf("unexpected error reading annotations: %v", err)
	}

	return mapping
}
