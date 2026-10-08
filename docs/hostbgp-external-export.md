# Explicit HostBGP extra-prefix export

This branch extends Fabric v0.133.0. It allows an ExternalPeering to import
selected HostBGP extra prefixes into its External VRF, where existing external
BGP policy can advertise the learned routes. It does not originate an aggregate
or install static return routes: a withdrawn node/service /32 disappears from
the export when its last usable VPC route disappears.

Existing peerings retain their current behavior. Extra prefixes remain private
unless explicitly selected. This avoids exporting NCCL/RoCE rail prefixes as a
side effect of permitting a subnet's primary range.

## Configuration

Given a VPC subnet with primary range 10.0.4.0/22 and an extra prefix
203.0.113.0/24 accepting only /32 advertisements, use the following example.
The names are placeholders; the public prefix is a documentation-only range.
Replace them with the deployment's resource names and allocated address ranges:

```yaml
apiVersion: vpc.githedgehog.com/v1beta1
kind: ExternalPeering
metadata:
  name: example-public-peering
  namespace: default
spec:
  permit:
    vpc:
      name: example-vpc
      subnets: []
      hostBGPExtraPrefixes:
        - 203.0.113.0/24
    external:
      name: example-external
      prefixes: []
```

`subnets: []` exports no primary ranges. The new list independently selects
exact CIDR keys configured in `hostBGPExtraPrefixes` on any HostBGP subnet in
that VPC. It inherits each extra prefix's effective minimum/maximum lengths,
including subnet-level defaults. In this example, only /32 routes qualify;
the /24 itself cannot qualify.

The external's normal outbound policy still applies, so the public block must
belong to its IPv4Namespace. ExternalAttachment, BGP community policy and the
upstream aggregate announcement are separate prerequisites. Adding
`external.prefixes: [{prefix: 0.0.0.0/0}]` permits default *and all more-specific*
external routes into the VPC; constrain the upstream router's outbound policy
to exactly the default route if that is all the VPC should receive.

This patch does not change VPCPeering, Cilium SNAT, host address configuration,
or ingress firewall behavior. It does not support the loopback workaround for
local inter-VRF forwarding: admission rejects that combination, and the planner
also rejects it if admission was bypassed. That workaround synthesizes static
aggregates and cannot preserve dynamic HostBGP /32 withdrawal semantics with
this small change. Deployments using this feature must support native inter-VRF
route leaking.

## Implementation

- API: explicit prefix selection, canonical IPv4/duplicate checks, resolution
  against configured HostBGP extras, inherited prefix-length bounds.
- Controller: allocate prefix-list sequence IDs for selected extra prefixes.
- Agent planner: extend the External VRF import prefix list with those bounds.
- Generated CRDs: both ExternalPeering and the embedded peering in Agent.
- Tests: public-only export, unchanged legacy behavior, inherited lengths,
  unknown prefixes, invalid/missing catalog IDs, deepcopy isolation and
  unsupported loopback mode. A separate planner test verifies tagged and
  untagged HostBGP subnets sharing one unbundled connection and one VPC VRF.

## Self-deployment assessment (not applied)

The feature needs updated API schemas, controller and switch agents. Updating
only the controller does not change the planner running on a leaf. Updating
only agents does not provide the new admission/schema or catalog allocation.

Use a dedicated version such as `v0.133.0-custom.1`, built from this release
branch. Build Linux/amd64 controller and agent binaries with the same embedded
version. Package the generated CRDs as the `fabric-api` Helm chart, the
controller as the `fabric` image and `fabric` Helm chart, and the agent as an
ORAS artifact containing a file named `agent`. The upstream justfile contains
the build/package/push recipes. Docker/Helm packaging has not been validated by
the local Go test/build checks.

For an airgapped deployment, copy the required artifact kinds into the local
registry under the configured Fabric repository paths. For example, beneath
`<registry>/<repository-prefix>/fabric`:

- `charts/fabric-api:<custom-version>`
- `charts/fabric:<custom-version>`
- `fabric:<custom-version>` (controller container image)
- `agent:<custom-version>` (ORAS binary artifact)

Retain the upstream artifacts for rollback. Use Fabricator's version overrides
for `fabric.api`, `fabric.controller` and `fabric.agent`; keep NOS, boot and
DHCPD at their installed versions. Merely patching a Deployment image bypasses
Fabricator's desired state and can be reverted by reconciliation.

Important canary detail: AgentReconciler sets every Agent's default desired
version to the controller's embedded version. A custom controller therefore
requests custom agents across the fabric. Existing `Agent.spec.version.override`
is preserved by reconciliation and preferred by the agent upgrader. Pin
non-canary agents to their current version before changing the controller,
then upgrade the border leaves deliberately. Any leaf that imports the selected
routes into an External VRF must run the patched planner. Choose the border
leaves participating in the external peering as the initial canary targets.

AgentUpgrade downloads the binary and runs a desired-state dry-run before
replacing the installed agent. This checks plan compatibility, not packet
forwarding. Do not treat a small source diff as a zero-risk fabric rollout.
This change does not require installing a different SONiC/NOS image, but agent
process updates and route-policy edits still warrant a controlled test.

Rollback order: first remove the new extra-prefix export selections while the
patched controller/agents can process them; confirm route withdrawal; restore
the original controller and agent version defaults/overrides; restore the API
chart last. Keep ingress/default-route activation separate until the node's
SNAT and service return paths have passed end-to-end tests.
