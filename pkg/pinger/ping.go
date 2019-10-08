package pinger

import (
	goping "github.com/sparrc/go-ping"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
	"math"
	"net"
	"os/exec"
	"sync"
	"time"
)

func StartPinger(config *Configuration) {
	for {
		checkOvs(config)
		checkOvnController(config)
		ping(config)
		if config.Mode != "server" {
			break
		}
		time.Sleep(time.Duration(config.Interval) * time.Second)
	}
}

func ping(config *Configuration) {
	pingNodes(config)
	pingPods(config)
	nslookup(config)
}

func pingNodes(config *Configuration) {
	klog.Infof("start to check node connectivity")
	nodes, err := config.KubeClient.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to list nodes, %v", err)
		return
	}
	wg := sync.WaitGroup{}
	for _, no := range nodes.Items {
		for _, addr := range no.Status.Addresses {
			if addr.Type == v1.NodeInternalIP {
				wg.Add(1)
				go func(nodeIP, nodeName string) {
					defer wg.Done()
					pinger, err := goping.NewPinger(nodeIP)
					if err != nil {
						klog.Errorf("failed to init pinger, %v", err)
						return
					}
					pinger.SetPrivileged(true)
					pinger.Count = 5
					pinger.Run()
					stats := pinger.Statistics()
					klog.Infof("ping node: %s %s, count: %d, loss rate %.2f%%, average rtt %.2fms",
						nodeName, nodeIP, pinger.Count, math.Abs(stats.PacketLoss)*100, float64(stats.AvgRtt)/float64(time.Millisecond))
					SetNodePingMetrics(
						config.NodeName,
						config.HostIP,
						config.PodName,
						no.Name, addr.Address,
						float64(stats.AvgRtt)/float64(time.Millisecond),
						int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))))
				}(addr.Address, no.Name)
			}
		}
	}
	wg.Wait()
}

func pingPods(config *Configuration) {
	klog.Infof("start to check pod connectivity")
	ds, err := config.KubeClient.AppsV1().DaemonSets(config.DaemonSetNamespace).Get(config.DaemonSetName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get peer ds: %v", err)
		return
	}
	pods, err := config.KubeClient.CoreV1().Pods(config.DaemonSetNamespace).List(metav1.ListOptions{LabelSelector: labels.Set(ds.Spec.Selector.MatchLabels).String()})
	if err != nil {
		klog.Errorf("failed to list peer pods: %v", err)
		return
	}

	wg := sync.WaitGroup{}
	for _, pod := range pods.Items {
		if pod.Status.PodIP != "" {
			wg.Add(1)
			go func(podIp, podName, nodeIP, nodeName string) {
				defer wg.Done()
				pinger, err := goping.NewPinger(podIp)
				if err != nil {
					klog.Errorf("failed to init pinger, %v", err)
					return
				}
				pinger.SetPrivileged(true)
				pinger.Count = 5
				pinger.Run()
				stats := pinger.Statistics()
				klog.Infof("ping pod: %s %s, count: %d, loss rate %.2f, average rtt %.2fms",
					podName, podIp, pinger.Count, math.Abs(stats.PacketLoss)*100, float64(stats.AvgRtt)/float64(time.Millisecond))
				SetPodPingMetrics(
					config.NodeName,
					config.HostIP,
					config.PodName,
					nodeName,
					nodeIP,
					podIp,
					float64(stats.AvgRtt)/float64(time.Millisecond),
					int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))))
			}(pod.Status.PodIP, pod.Name, pod.Spec.NodeName, pod.Status.HostIP)
		}
	}
	wg.Wait()
}

func nslookup(config *Configuration) {
	klog.Infof("start to check dns connectivity")
	t1 := time.Now()
	addrs, err := net.LookupHost(config.DNS)
	elpased := time.Since(t1)
	if err != nil {
		klog.Errorf("failed to resolve dns %s, %v", config.DNS, err)
		SetDnsUnhealthyMetrics(config.NodeName)
		return
	}
	SetDnsHealthyMetrics(config.NodeName, float64(elpased)/float64(time.Millisecond))
	klog.Infof("resolve dns %s to %v in %.2fms", config.DNS, addrs, float64(elpased)/float64(time.Millisecond))
}

func checkOvs(config *Configuration) {
	output, err := exec.Command("/usr/share/openvswitch/scripts/ovs-ctl", "status").CombinedOutput()
	if err != nil {
		klog.Errorf("check ovs status failed %v, %s", err, string(output))
		SetOvsDownMetrics(config.NodeName)
		return
	}
	klog.Infof("ovs-vswitchd and ovsdb are up")
	SetOvsUpMetrics(config.NodeName)
	return
}

func checkOvnController(config *Configuration) {
	output, err := exec.Command("/usr/share/openvswitch/scripts/ovn-ctl", "status_controller").CombinedOutput()
	if err != nil {
		klog.Errorf("check ovn_controller status failed %v, %s", err, string(output))
		SetOvnControllerDownMetrics(config.NodeName)
		return
	}
	klog.Infof("ovn_controller is up")
	SetOvnControllerUpMetrics(config.NodeName)
}
