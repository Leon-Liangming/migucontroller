package main

import (
	"fmt"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"io/ioutil"

	"github.com/spf13/pflag"
	"github.com/golang/glog"

	"migucontroller/iamtoken"
	"migucontroller/controller"

	"k8s.io/kubernetes/pkg/client/unversioned/clientcmd"
	"k8s.io/kubernetes/pkg/api"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/client/restclient"
	"os/signal"
	"syscall"
	"time"
)
var (
	flags = pflag.NewFlagSet("", pflag.ExitOnError)

	accessKey = flags.String("access-key", "", `request to kubernetes apiserver's accesskey.`)
	secretKey = flags.String("secret-key", "", `request to kubernetes apiserver's secretkey.`)
	tokenNamespace = flags.String("token-namespace", "op_svc_cfe", "get which namespace's token.")
	iamServerAddress = flags.String("iam-sersver-address", "", "iam server address, use accessKey and secretKey to get token from here.")

	kubernetesApiserverIP = flags.String("kube-apiserver-ip", "", "kubernetes apisersver ip")
	kubernetesApiserverPort = flags.String("kube-apiserver-port", "", "kubernetes apisersver port")

	watchNamespace = flags.String("watch-namespace", api.NamespaceAll,
		`Namespace to watch for Ingress. Default is to watch all namespaces`)
	resyncPeriod = flags.Duration("sync-period", 30*time.Second,
		`Relist and confirm cloud resources this often.`)

	nginxTemplate      = flags.String("nginx-template", "./upstream.tmpl", "nginx config template file.")
	upstreamConfigPath = flags.String("upstream-config-path", "./upstream.conf", "nginx upstream config file.")
	nginxConfigPath    = flags.String("nginx-config-path", "./nginx.conf", "nginx config file.")
)

func main() {
	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(os.Args)
	if *kubernetesApiserverIP =="" {
		glog.Fatalf("Please set kubernetes apiserver IP.")
	}

	if *kubernetesApiserverPort == "" {
		glog.Fatalf("Please set kubernetes apiserver Port.")
	}

	os.Setenv("KUBERNETES_SERVICE_HOST", *kubernetesApiserverIP)
	os.Setenv("KUBERNETES_SERVICE_PORT", *kubernetesApiserverPort)

	iamAddress := fmt.Sprintf("https://%v", *iamServerAddress)
	//获取token，并以KuberClient要求的形式写入到本地文件，连接访问时使用。
	getTokenForKuberapiserver(iamAddress)
	//os.Setenv(api.KubernetesServiceTokenDir, tokenDir)


	//通过设置环境变量获取kubernete apiserver的访问地址和token的内容。
	kubeconfig, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		glog.Fatalf("get kubeconfig failed. %v", err)
	}

	var kubeClient *clientset.Clientset
	kubeClient, err = clientset.NewForConfig(restclient.AddUserAgent(kubeconfig, "migu-nginx-controller"))
	if err != nil {
		glog.Fatalf("Invalid API configuration: %v", err)
	}

	miguController := controller.NewController(kubeClient, *watchNamespace, *resyncPeriod, *nginxTemplate, *upstreamConfigPath, *nginxConfigPath)
	go handleSigterm(miguController)

	miguController.Run()

	for {
		glog.Infof("Handled quit, awaiting pod deletion")
		time.Sleep(30 * time.Second)
	}
}

func getCurrentDirectory() string {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		glog.Fatal(err)
	}
	return strings.Replace(dir, "\\", "/", -1)
}

// 获取token并写入文件，用于kubeclient的对接
func getTokenForKuberapiserver(iamAddress string) (string) {
	token, err := iamtoken.GetToken(iamAddress, *accessKey, *secretKey, *tokenNamespace)
	if err!= nil {
		glog.Fatalf("Failed to get token: %v", err)
	}

	tokenDir := "/var/run/secrets/kubernetes.io/serviceaccount/"
	os.MkdirAll(tokenDir, 777)
	tokenPath := fmt.Sprintf("%v/%v", tokenDir, api.ServiceAccountTokenKey)
	err = ioutil.WriteFile(tokenPath, []byte(token), 0644)
	if err != nil {
		glog.Fatalf("Error write token to kubeconfig: %v", err)
		return ""
	}
	glog.Infof("Get Token success.")
	return tokenDir
}

func handleSigterm(lbc *controller.Controller) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGTERM)
	<-signalChan
	glog.Infof("Received SIGTERM, shutting down")

	exitCode := 0
	if err := lbc.Stop(); err != nil {
		glog.Infof("Error during shutdown %v", err)
		exitCode = 1
	}

	glog.Infof("Exiting with %v", exitCode)
	os.Exit(exitCode)
}

