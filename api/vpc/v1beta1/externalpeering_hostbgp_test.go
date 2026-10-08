// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.githedgehog.com/fabric/api/meta"
	"go.githedgehog.com/fabric/api/vpc/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestExternalPeeringHostBGPExports(t *testing.T) {
	const prefix = "203.0.113.0/24"
	for _, tt := range []struct {
		name      string
		selection []string
		hostBGP   bool
		loopback  bool
		err       string
	}{
		{name: "public-only selection", selection: []string{prefix}, hostBGP: true},
		{name: "omitted selection", hostBGP: true},
		{name: "unknown prefix", selection: []string{"198.51.100.0/24"}, hostBGP: true, err: "does not have hostBGP extra prefix"},
		{name: "not a hostBGP subnet", selection: []string{prefix}, err: "does not have hostBGP extra prefix"},
		{name: "duplicate", selection: []string{prefix, prefix}, hostBGP: true, err: "duplicate"},
		{name: "invalid prefix", selection: []string{"bad"}, hostBGP: true, err: "canonical IPv4 prefixes"},
		{name: "noncanonical prefix", selection: []string{"203.0.113.10/24"}, hostBGP: true, err: "canonical IPv4 prefixes"},
		{name: "IPv6 prefix", selection: []string{"2001:db8::/64"}, hostBGP: true, err: "canonical IPv4 prefixes"},
		{name: "unsupported loopback", selection: []string{prefix}, hostBGP: true, loopback: true, err: "native inter-VRF route leaking"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			vpc := &v1beta1.VPC{
				ObjectMeta: kmetav1.ObjectMeta{Name: "vpc-01", Namespace: "default"},
				Spec: v1beta1.VPCSpec{IPv4Namespace: "default", Subnets: map[string]*v1beta1.VPCSubnet{
					"subnet-a": {
						Subnet: "10.0.4.0/22", HostBGP: tt.hostBGP,
						HostBGPExtraPrefixes: map[string]v1beta1.VPCSubnetHostBGPPrefix{prefix: {}},
					},
				}},
			}
			ext := &v1beta1.External{ObjectMeta: kmetav1.ObjectMeta{Name: "external-01", Namespace: "default"}, Spec: v1beta1.ExternalSpec{IPv4Namespace: "default"}}
			scheme := runtime.NewScheme()
			require.NoError(t, v1beta1.AddToScheme(scheme))
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vpc, ext).Build()
			peering := extPeeringGen("public-export", func(p *v1beta1.ExternalPeering) {
				p.Spec.Permit.VPC.Subnets = nil
				p.Spec.Permit.VPC.HostBGPExtraPrefixes = tt.selection
			})
			_, err := peering.Validate(t.Context(), kube, &meta.FabricConfig{LoopbackWorkaround: tt.loopback})
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			resolved, err := peering.Spec.Permit.VPC.ResolveHostBGPExtraPrefixes(vpc.Spec)
			require.NoError(t, err)
			if len(tt.selection) > 0 {
				require.Equal(t, v1beta1.VPCSubnetHostBGPPrefix{MinPrefixLen: 32, MaxPrefixLen: 32}, resolved[prefix])
			}
			copy := peering.DeepCopy()
			if len(copy.Spec.Permit.VPC.HostBGPExtraPrefixes) > 0 {
				copy.Spec.Permit.VPC.HostBGPExtraPrefixes[0] = "198.51.100.0/24"
				require.Equal(t, prefix, peering.Spec.Permit.VPC.HostBGPExtraPrefixes[0])
			}
		})
	}
}
