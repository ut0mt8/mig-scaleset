package scaler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"
)

type migError = computepb.InstanceManagedByIgmError

// mig wraps the calls made on the regional MIG.
type mig struct {
	client  *compute.RegionInstanceGroupManagersClient
	project string
	region  string
	name    string
	prefix  string
}

// create adds VMs to the MIG, which also raises its target size. The MIG
// picks the zone of each VM.
func (m *mig) create(ctx context.Context, configs []*computepb.PerInstanceConfig) (*compute.Operation, error) {
	body := &computepb.RegionInstanceGroupManagersCreateInstancesRequest{
		Instances: configs,
	}

	op, err := m.client.CreateInstances(ctx, &computepb.CreateInstancesRegionInstanceGroupManagerRequest{
		Project:              m.project,
		Region:               m.region,
		InstanceGroupManager: m.name,
		RegionInstanceGroupManagersCreateInstancesRequestResource: body,
	})
	if err != nil {
		return nil, fmt.Errorf("createInstances on %q: %w", m.name, err)
	}
	return op, nil
}

// delete removes VMs by instance URL and lowers the target size.
// Runner VMs are disposable, so graceful shutdown is skipped.
func (m *mig) delete(ctx context.Context, urls []string) error {
	if len(urls) == 0 {
		return nil
	}

	body := &computepb.RegionInstanceGroupManagersDeleteInstancesRequest{
		Instances: urls,
		// Ignore VMs already gone or being deleted.
		SkipInstancesOnValidationError: proto.Bool(true),
	}

	_, err := m.client.DeleteInstances(ctx, &computepb.DeleteInstancesRegionInstanceGroupManagerRequest{
		Project:              m.project,
		Region:               m.region,
		InstanceGroupManager: m.name,
		NoGracefulShutdown:   proto.Bool(true),
		RegionInstanceGroupManagersDeleteInstancesRequestResource: body,
	})
	if err != nil {
		return fmt.Errorf("deleteInstances on %q: %w", m.name, err)
	}
	return nil
}

// deleteByName looks up the instance URLs first: in a regional MIG a VM can
// be in any zone.
func (m *mig) deleteByName(ctx context.Context, names []string) error {
	vms, _, err := m.list(ctx)
	if err != nil {
		return err
	}

	urls := make([]string, 0, len(names))
	for _, name := range names {
		if vm, ok := vms[name]; ok {
			urls = append(urls, vm.GetInstance())
		}
	}
	return m.delete(ctx, urls)
}

// list returns our runner VMs by name, and the names of VMs without our
// prefix, which are never touched.
func (m *mig) list(ctx context.Context) (map[string]*computepb.ManagedInstance, []string, error) {
	it := m.client.ListManagedInstances(ctx, &computepb.ListManagedInstancesRegionInstanceGroupManagersRequest{
		Project:              m.project,
		Region:               m.region,
		InstanceGroupManager: m.name,
	})

	vms := map[string]*computepb.ManagedInstance{}
	foreign := []string{}

	for {
		vm, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return vms, foreign, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("listing instances of %q: %w", m.name, err)
		}

		name := vm.GetName()
		switch {
		case vm.GetCurrentAction() == "DELETING":
			continue
		case strings.HasPrefix(name, m.prefix+"-"):
			vms[name] = vm
		default:
			foreign = append(foreign, name)
		}
	}
}

// lastErrors returns the latest MIG error for each given VM name. Creation
// failures (quota, capacity, bad template) only show up there.
func (m *mig) lastErrors(ctx context.Context, names []string) (map[string]*migError, error) {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}

	it := m.client.ListErrors(ctx, &computepb.ListErrorsRegionInstanceGroupManagersRequest{
		Project:              m.project,
		Region:               m.region,
		InstanceGroupManager: m.name,
	})

	latest := map[string]*migError{}
	for {
		migErr, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return latest, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing errors of %q: %w", m.name, err)
		}

		url := migErr.GetInstanceActionDetails().GetInstance()
		name := url[strings.LastIndex(url, "/")+1:]
		if !wanted[name] {
			continue
		}

		previous, seen := latest[name]
		if !seen || migErr.GetTimestamp() > previous.GetTimestamp() {
			latest[name] = migErr
		}
	}
}

// waitOperation also fails when the finished operation reports an error.
func waitOperation(ctx context.Context, op *compute.Operation) error {
	if err := op.Wait(ctx); err != nil {
		return err
	}
	if opErr := op.Proto().GetError(); opErr != nil {
		return fmt.Errorf("operation failed: %v", opErr.GetErrors())
	}
	return nil
}

func isDown(status string) bool {
	switch status {
	case "STOPPING", "STOPPED", "TERMINATED", "SUSPENDING", "SUSPENDED":
		return true
	}
	return false
}
