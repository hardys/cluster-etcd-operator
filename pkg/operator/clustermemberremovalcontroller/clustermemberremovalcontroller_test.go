package clustermemberremovalcontroller

import (
	"context"
	"testing"

	"go.etcd.io/etcd/api/v3/etcdserverpb"

	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	machinelistersv1beta1 "github.com/openshift/client-go/machine/listers/machine/v1beta1"
	"github.com/openshift/cluster-etcd-operator/pkg/etcdcli"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfakeclient "k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func TestAttemptToRemoveLearningMember(t *testing.T) {
	scenarios := []struct {
		name                                     string
		initialObjectsForMachineLister           []runtime.Object
		initialObjectsForConfigMapTargetNSLister []runtime.Object
		initialEtcdMemberList                    []*etcdserverpb.Member
		validateFn                               func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient)
	}{
		{
			name: "learning member pending deletion is removed",
			initialObjectsForMachineLister: func() []runtime.Object {
				m4 := machineWithHooksFor("m-4", "10.0.139.81")
				m4.DeletionTimestamp = &metav1.Time{}
				machines := wellKnownMasterMachines()
				machines = append(machines, m4)
				return machines
			}(),
			initialObjectsForConfigMapTargetNSLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			initialEtcdMemberList: func() []*etcdserverpb.Member {
				members := append(wellKnownEtcdMemberList(), &etcdserverpb.Member{
					Name:      "m-4",
					ID:        4,
					IsLearner: true,
					PeerURLs:  []string{"https://10.0.139.81:1234"},
				})
				return members
			}(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 3 {
					t.Errorf("expected exactly 3 members, got %v", len(memberList))
				}
				for _, member := range memberList {
					if member.ID == 4 {
						t.Fatalf("expected the member: %v to be removed from the etcd cluster but it wasn't", member)
					}
				}
			},
		},

		{
			name:                  "voting member pending deletion is NOT removed",
			initialEtcdMemberList: wellKnownEtcdMemberList(),
			initialObjectsForMachineLister: func() []runtime.Object {
				machines := wellKnownMasterMachines()
				m0 := machines[0].(*machinev1beta1.Machine)
				m0.DeletionTimestamp = &metav1.Time{}
				return machines
			}(),
			initialObjectsForConfigMapTargetNSLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 3 {
					t.Errorf("expected exactly 3 members, got %v", len(memberList))
				}
			},
		},

		{
			name: "excessive voting member pending deletion is NOT removed",
			initialEtcdMemberList: func() []*etcdserverpb.Member {
				members := append(wellKnownEtcdMemberList(), &etcdserverpb.Member{
					Name:     "m-4",
					ID:       4,
					PeerURLs: []string{"https://10.0.139.81:1234"},
				})
				return members
			}(),
			initialObjectsForMachineLister: func() []runtime.Object {
				m4 := machineWithHooksFor("m-4", "10.0.139.81")
				m4.DeletionTimestamp = &metav1.Time{}
				machines := wellKnownMasterMachines()
				machines = append(machines, m4)
				return machines
			}(),
			initialObjectsForConfigMapTargetNSLister: func() []runtime.Object {
				cm := wellKnownEtcdEndpointsConfigMap()
				cm.Data["m-4"] = "10.0.139.81"
				return []runtime.Object{cm}
			}(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 4 {
					t.Errorf("expected exactly 4 members, got %v", len(memberList))
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// test data
			configMapTargetNSIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, initialObj := range scenario.initialObjectsForConfigMapTargetNSLister {
				configMapTargetNSIndexer.Add(initialObj)
			}
			configMapTargetNSLister := corev1listers.NewConfigMapLister(configMapTargetNSIndexer).ConfigMaps("openshift-etcd")

			machineIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, initialObj := range scenario.initialObjectsForMachineLister {
				machineIndexer.Add(initialObj)
			}
			machineLister := machinelistersv1beta1.NewMachineLister(machineIndexer)
			machineSelector, err := labels.Parse("machine.openshift.io/cluster-api-machine-role=master")
			if err != nil {
				t.Fatal(err)
			}
			fakeEtcdClient, err := etcdcli.NewFakeEtcdClient(scenario.initialEtcdMemberList)
			if err != nil {
				t.Fatal(err)
			}

			// act
			target := clusterMemberRemovalController{
				etcdClient:                        fakeEtcdClient,
				masterMachineLister:               machineLister,
				masterMachineSelector:             machineSelector,
				configMapListerForTargetNamespace: configMapTargetNSLister,
			}
			err = target.attemptToRemoveLearningMember(context.TODO())
			if err != nil {
				t.Fatal(err)
			}
			if scenario.validateFn != nil {
				scenario.validateFn(t, fakeEtcdClient)
			}
		})
	}
}

func TestClusterMemberRemovalController(t *testing.T) {
	alwaysTrueIsFunctionalMachineAPIFn := func() (bool, error) { return true, nil }

	scenarios := []struct {
		name                             string
		isFunctionalMachineAPIFn         func() (bool, error)
		initialObjectsForConfigMapLister []runtime.Object
		initialObjectsForNodeLister      []runtime.Object
		initialObjectsForMachineLister   []runtime.Object
		initialEtcdMemberList            []*etcdserverpb.Member
		validateFn                       func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient)
		serviceNetwork                   string
	}{
		// scenario 1
		{
			name:                             "happy path: an etcd member has a corresponding machine and node resources",
			serviceNetwork:                   "172.30.0.0/16",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			initialObjectsForNodeLister:      []runtime.Object{wellKnownMasterNode()},
			initialObjectsForMachineLister:   []runtime.Object{wellKnownMasterMachine()},
			initialEtcdMemberList:            wellKnownEtcdMemberList(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},

		// scenario 1 (ipv6)
		{
			name:                             "happy path (ipv6): an etcd member has a corresponding machine and node resources",
			serviceNetwork:                   "fd02::/112",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMapIpv6()},
			initialObjectsForNodeLister:      []runtime.Object{wellKnownMasterNodeIpv6()},
			initialObjectsForMachineLister:   []runtime.Object{wellKnownMasterMachineIpv6()},
			initialEtcdMemberList:            wellKnownEtcdMemberListIpv6(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},

		// scenario 2
		{
			name:                             "an etcd member doesn't have a corresponding machine nor node resource and it is removed",
			serviceNetwork:                   "172.30.0.0/16",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			initialEtcdMemberList:            wellKnownEtcdMemberList(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 0 {
					t.Errorf("expected an empty member list, got %v", memberList)
				}
			},
		},

		// scenario 2 (ipv6)
		{
			name:                             "(ipv6) an etcd member doesn't have a corresponding machine nor node resource and it is removed",
			serviceNetwork:                   "fd02::/112",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMapIpv6()},
			initialEtcdMemberList:            wellKnownEtcdMemberListIpv6(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 0 {
					t.Errorf("expected an empty member list, got %v", memberList)
				}
			},
		},

		// scenario 3
		{
			name:                             "an etcd member with only a corresponding machine resource is not removed",
			serviceNetwork:                   "172.30.0.0/16",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			initialObjectsForMachineLister:   []runtime.Object{wellKnownMasterMachine()},
			initialEtcdMemberList:            wellKnownEtcdMemberList(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},

		// scenario 3 (ipv6)
		{
			name:                             "(ipv6) an etcd member with only a corresponding machine resource is not removed",
			serviceNetwork:                   "fd02::/112",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMapIpv6()},
			initialObjectsForMachineLister:   []runtime.Object{wellKnownMasterMachineIpv6()},
			initialEtcdMemberList:            wellKnownEtcdMemberListIpv6(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},

		// scenario 4
		{
			name:                             "an etcd member with only a corresponding node resource is not removed",
			serviceNetwork:                   "172.30.0.0/16",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMap()},
			initialObjectsForNodeLister:      []runtime.Object{wellKnownMasterNode()},
			initialEtcdMemberList:            wellKnownEtcdMemberList(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},

		// scenario 4 (ipv6)
		{
			name:                             "(ipv6) an etcd member with only a corresponding node resource is not removed",
			serviceNetwork:                   "fd02::/112",
			isFunctionalMachineAPIFn:         alwaysTrueIsFunctionalMachineAPIFn,
			initialObjectsForConfigMapLister: []runtime.Object{wellKnownEtcdEndpointsConfigMapIpv6()},
			initialObjectsForNodeLister:      []runtime.Object{wellKnownMasterNodeIpv6()},
			initialEtcdMemberList:            wellKnownEtcdMemberListIpv6(),
			validateFn: func(t *testing.T, fakeEtcdClient etcdcli.EtcdClient) {
				memberList, err := fakeEtcdClient.MemberList(context.TODO())
				if err != nil {
					t.Fatal(err)
				}
				if len(memberList) != 1 {
					t.Errorf("expected exactly one etcd member, got %v", memberList)
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// test data
			eventRecorder := events.NewRecorder(k8sfakeclient.NewSimpleClientset().CoreV1().Events("operator"), "test-cluster-member-removal-controller", &corev1.ObjectReference{})
			fakeMachineAPIChecker := &fakeMachineAPI{isMachineAPIFunctional: scenario.isFunctionalMachineAPIFn}

			configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, initialObj := range scenario.initialObjectsForConfigMapLister {
				configMapIndexer.Add(initialObj)
			}
			configMapLister := corev1listers.NewConfigMapLister(configMapIndexer).ConfigMaps("openshift-etcd")

			nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, initialObj := range scenario.initialObjectsForNodeLister {
				nodeIndexer.Add(initialObj)
			}
			nodeLister := corev1listers.NewNodeLister(nodeIndexer)
			nodeSelector, err := labels.Parse("node-role.kubernetes.io/master")
			if err != nil {
				t.Fatal(err)
			}

			networkIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			networkIndexer.Add(&configv1.Network{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.NetworkSpec{ServiceNetwork: []string{scenario.serviceNetwork}}})
			networkLister := configv1listers.NewNetworkLister(networkIndexer)

			machineIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, initialObj := range scenario.initialObjectsForMachineLister {
				machineIndexer.Add(initialObj)
			}
			machineLister := machinelistersv1beta1.NewMachineLister(machineIndexer)
			machineSelector, err := labels.Parse("machine.openshift.io/cluster-api-machine-role=master")
			if err != nil {
				t.Fatal(err)
			}
			fakeEtcdClient, err := etcdcli.NewFakeEtcdClient(scenario.initialEtcdMemberList)
			if err != nil {
				t.Fatal(err)
			}

			// act
			target := clusterMemberRemovalController{
				etcdClient:                        fakeEtcdClient,
				machineAPIChecker:                 fakeMachineAPIChecker,
				configMapListerForTargetNamespace: configMapLister,
				masterNodeSelector:                nodeSelector,
				masterNodeLister:                  nodeLister,
				masterMachineSelector:             machineSelector,
				masterMachineLister:               machineLister,
				networkLister:                     networkLister,
			}
			err = target.sync(context.TODO(), factory.NewSyncContext("test-cluster-member-removal-controller", eventRecorder))
			if err != nil {
				t.Fatal(err)
			}
			if scenario.validateFn != nil {
				scenario.validateFn(t, fakeEtcdClient)
			}
		})
	}
}

func wellKnownEtcdEndpointsConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd-endpoints", Namespace: "openshift-etcd"},
		Data: map[string]string{
			"m-1": "10.0.139.78",
			"m-2": "10.0.139.79",
			"m-3": "10.0.139.80",
		},
	}
}

func wellKnownEtcdEndpointsConfigMapIpv6() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "etcd-endpoints", Namespace: "openshift-etcd"},
		Data: map[string]string{
			"m-0": "fd2e:6f44:5dd8:c956::16",
		},
	}
}

func wellKnownEtcdMemberList() []*etcdserverpb.Member {
	return []*etcdserverpb.Member{
		{
			Name:     "m-1",
			ID:       1,
			PeerURLs: []string{"https://10.0.139.78:1234"},
		},
		{
			Name:     "m-2",
			ID:       2,
			PeerURLs: []string{"https://10.0.139.79:1234"},
		},
		{
			Name:     "m-3",
			ID:       3,
			PeerURLs: []string{"https://10.0.139.80:1234"},
		},
	}
}

func wellKnownEtcdMemberListIpv6() []*etcdserverpb.Member {
	return []*etcdserverpb.Member{
		{
			Name:     "m-0",
			ID:       8,
			PeerURLs: []string{"https://[fd2e:6f44:5dd8:c956::16]:1234"},
		},
	}
}

func wellKnownMasterNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "m-0", Labels: map[string]string{"node-role.kubernetes.io/master": ""}},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: "10.0.139.78",
			},
		}},
	}
}

func wellKnownMasterNodeIpv6() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "m-0", Labels: map[string]string{"node-role.kubernetes.io/master": ""}},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: "fd2e:6f44:5dd8:c956::16",
			},
		}},
	}
}

func wellKnownMasterMachine() *machinev1beta1.Machine {
	return &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "m-0", Labels: map[string]string{"machine.openshift.io/cluster-api-machine-role": "master"}},
		Status: machinev1beta1.MachineStatus{Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: "10.0.139.78",
			},
		}},
	}
}

func wellKnownMasterMachineIpv6() *machinev1beta1.Machine {
	return &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "m-0", Labels: map[string]string{"machine.openshift.io/cluster-api-machine-role": "master"}},
		Status: machinev1beta1.MachineStatus{Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: "fd2e:6f44:5dd8:c956::16",
			},
		}},
	}
}

type fakeMachineAPI struct {
	isMachineAPIFunctional func() (bool, error)
}

func (dm *fakeMachineAPI) IsFunctional() (bool, error) {
	return dm.isMachineAPIFunctional()
}

func machineFor(name, internalIP string) *machinev1beta1.Machine {
	return &machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"machine.openshift.io/cluster-api-machine-role": "master"}},
		Status: machinev1beta1.MachineStatus{Addresses: []corev1.NodeAddress{
			{
				Type:    corev1.NodeInternalIP,
				Address: internalIP,
			},
		}},
	}
}

func machineWithHooksFor(name, internalIP string) *machinev1beta1.Machine {
	m := machineFor(name, internalIP)
	m.Spec.LifecycleHooks.PreDrain = append(m.Spec.LifecycleHooks.PreDrain, machinev1beta1.LifecycleHook{Name: "EtcdQuorumOperator", Owner: "clusteroperator/etcd"})
	return m
}

func wellKnownMasterMachines() []runtime.Object {
	return []runtime.Object{
		machineWithHooksFor("m-1", "10.0.139.78"),
		machineWithHooksFor("m-2", "10.0.139.79"),
		machineWithHooksFor("m-3", "10.0.139.80"),
	}
}
