// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package bcm

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	"go.githedgehog.com/fabric/pkg/agent/dozer"
	kyaml "sigs.k8s.io/yaml"
)

func TestPlanHostBGPExtraPrefixExport(t *testing.T) {
	const prefix = "203.0.113.0/24"
	newAgent := func(t *testing.T) *agentapi.Agent {
		t.Helper()
		data, err := os.ReadFile("testdata/l3vni-leaf-01.in.agent.yaml")
		require.NoError(t, err)
		ag := &agentapi.Agent{}
		require.NoError(t, kyaml.Unmarshal(data, ag))
		ipns := ag.Spec.IPv4Namespaces["default"]
		ipns.Subnets = append(ipns.Subnets, prefix)
		ag.Spec.IPv4Namespaces["default"] = ipns
		ag.Spec.VPCs["vpc-02"].Subnets["default"].HostBGPExtraPrefixes[prefix] = vpcapi.VPCSubnetHostBGPPrefix{MinPrefixLen: 32, MaxPrefixLen: 32}
		ag.Spec.Catalog.SubnetIDs[prefix] = 60000
		return ag
	}
	selectPrefix := func(ag *agentapi.Agent) {
		peering := ag.Spec.ExternalPeerings["vpc-02--ext-snp-02"]
		peering.Permit.VPC.Subnets = nil // export the extra public prefix without the private primary
		peering.Permit.VPC.HostBGPExtraPrefixes = []string{prefix}
		ag.Spec.ExternalPeerings["vpc-02--ext-snp-02"] = peering
	}
	plan := func(t *testing.T, ag *agentapi.Agent) *dozer.Spec {
		t.Helper()
		spec, err := (&BroadcomProcessor{}).PlanDesiredState(t.Context(), ag)
		require.NoError(t, err)
		return spec
	}

	t.Run("existing peering does not export extras", func(t *testing.T) {
		ag := newAgent(t)
		spec := plan(t, ag)
		entries := spec.PrefixLists[extImportPrefixListName("ext-snp-02")].Prefixes
		require.Contains(t, entries, ag.Spec.Catalog.SubnetIDs["10.0.2.0/24"])
		require.NotContains(t, entries, uint32(60000))
	})
	t.Run("public-only selection leaks learned routes without an aggregate", func(t *testing.T) {
		ag := newAgent(t)
		selectPrefix(ag)
		spec := plan(t, ag)
		entries := spec.PrefixLists[extImportPrefixListName("ext-snp-02")].Prefixes
		require.Len(t, entries, 1)
		require.Equal(t, dozer.SpecPrefixListPrefix{Prefix: prefix, Ge: 32, Le: 32}, entries[60000].Prefix)
		require.Equal(t, dozer.SpecPrefixListActionPermit, entries[60000].Action)
		extVRF := spec.VRFs[extVrfName("ext-snp-02")]
		require.Contains(t, extVRF.BGP.IPv4Unicast.ImportVRFs, vpcVrfName("vpc-02"))
		require.Equal(t, extImportRouteMapName("ext-snp-02"), *extVRF.BGP.IPv4Unicast.ImportPolicy)
		require.NotContains(t, extVRF.StaticRoutes, prefix)
		require.NotContains(t, extVRF.BGP.IPv4Unicast.Networks, prefix)
	})
	t.Run("inherit prefix-length bounds", func(t *testing.T) {
		ag := newAgent(t)
		selectPrefix(ag)
		ag.Spec.VPCs["vpc-02"].Subnets["default"].HostBGPExtraPrefixes[prefix] = vpcapi.VPCSubnetHostBGPPrefix{}
		spec := plan(t, ag)
		require.Equal(t, dozer.SpecPrefixListPrefix{Prefix: prefix, Le: 32}, spec.PrefixLists[extImportPrefixListName("ext-snp-02")].Prefixes[60000].Prefix)
	})
	t.Run("missing catalog allocation fails closed", func(t *testing.T) {
		ag := newAgent(t)
		selectPrefix(ag)
		delete(ag.Spec.Catalog.SubnetIDs, prefix)
		_, err := (&BroadcomProcessor{}).PlanDesiredState(t.Context(), ag)
		require.ErrorContains(t, err, "invalid hostBGP export prefix id")
	})
	t.Run("unknown extra prefix fails closed", func(t *testing.T) {
		ag := newAgent(t)
		selectPrefix(ag)
		delete(ag.Spec.VPCs["vpc-02"].Subnets["default"].HostBGPExtraPrefixes, prefix)
		_, err := (&BroadcomProcessor{}).PlanDesiredState(t.Context(), ag)
		require.ErrorContains(t, err, "does not have hostBGP extra prefix")
	})
	t.Run("loopback workaround does not synthesize an aggregate", func(t *testing.T) {
		ag := newAgent(t)
		selectPrefix(ag)
		spec := plan(t, ag)
		ag.Spec.Config.LoopbackWorkaround = true
		err := planExternalPeerings(ag, spec)
		require.ErrorContains(t, err, "requires native inter-VRF route leaking")
	})
}

// Tagged and untagged hostBGP subnets can share an unbundled connection and a VPC VRF.
func TestPlanHostBGPDualSubnetAttachments(t *testing.T) {
	data, err := os.ReadFile("testdata/l3vni-leaf-01.in.agent.yaml")
	require.NoError(t, err)
	ag := &agentapi.Agent{}
	require.NoError(t, kyaml.Unmarshal(data, ag))
	vpc := ag.Spec.VPCs["vpc-01"]
	vpc.Subnets["default"].VLAN = 0
	vpc.Subnets["default"].HostBGPExtraPrefixes = nil
	vpc.Subnets["public"] = &vpcapi.VPCSubnet{Subnet: "203.0.113.0/24", VLAN: 1100, HostBGP: true}
	ag.Spec.VPCs["vpc-01"] = vpc
	ag.Spec.Catalog.SubnetIDs["203.0.113.0/24"] = 60000
	ag.Spec.Catalog.VPCSubnetVNIs["vpc-01"]["public"] = 302
	ipns := ag.Spec.IPv4Namespaces["default"]
	ipns.Subnets = append(ipns.Subnets, "203.0.113.0/24")
	ag.Spec.IPv4Namespaces["default"] = ipns
	ag.Spec.ConfiguredVPCSubnets["vpc-01/public"] = true
	publicAttach := ag.Spec.VPCAttachments["s3-v1-l1"]
	publicAttach.Subnet = "vpc-01/public"
	ag.Spec.VPCAttachments["public"] = publicAttach
	peering := ag.Spec.ExternalPeerings["vpc-02--ext-snp-02"]
	peering.Permit.VPC = vpcapi.ExternalPeeringSpecVPC{Name: "vpc-01", Subnets: []string{"public"}}
	ag.Spec.ExternalPeerings["vpc-02--ext-snp-02"] = peering
	spec, err := (&BroadcomProcessor{}).PlanDesiredState(t.Context(), ag)
	require.NoError(t, err)
	vrf := spec.VRFs[vpcVrfName("vpc-01")]
	privateAttach := ag.Spec.VPCAttachments["s3-v1-l1"]
	port := ag.Spec.Connections[privateAttach.Connection].Unbundled.Link.Switch.LocalPortName()
	port = ag.Spec.SwitchProfile.Ports[port].NOSName
	require.Contains(t, vrf.Interfaces, port)
	require.Contains(t, vrf.Interfaces, port+".1100")
	require.Contains(t, vrf.BGP.Neighbors, port)
	require.Contains(t, vrf.BGP.Neighbors, port+".1100")
	require.Equal(t, uint16(1100), *spec.Interfaces[port].Subinterfaces[1100].VLAN)
	entries := spec.PrefixLists[extImportPrefixListName("ext-snp-02")].Prefixes
	require.Len(t, entries, 1)
	require.Equal(t, "203.0.113.0/24", entries[60000].Prefix.Prefix)
}
