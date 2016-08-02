package flux

import (
	"errors"
	"time"

	"github.com/weaveworks/fluxy/automator"
	"github.com/weaveworks/fluxy/history"
	"github.com/weaveworks/fluxy/platform"
	"github.com/weaveworks/fluxy/platform/kubernetes"
	"github.com/weaveworks/fluxy/registry"
)

// DefaultNamespace is used when no namespace is provided to service methods.
const DefaultNamespace = "default"

// Service is the flux.Service, i.e. what is implemented by fluxd.
// It deals in (among other things) services on the platform.
type Service interface {
	// Images returns the images that are available in a repository.
	// Always in reverse chronological order, i.e. newest first.
	Images(repository string) ([]registry.Image, error)

	// ServiceImages returns a list of (container, images),
	// representing the running state (the container) along with the
	// potentially releasable state (the images)
	ServiceImages(namespace, service string) ([]ContainerImages, error)

	// Services returns the currently active services on the platform.
	Services(namespace string) ([]platform.Service, error)

	// History returns the release history of one or all services
	History(namespace, service string) (map[string]history.History, error)

	// Release migrates a service from its current image to a new image, derived
	// from the newDef definition. Right now, that needs to be the body of a
	// replication controller. A rolling-update is performed with the provided
	// updatePeriod. This call blocks until it's complete.
	Release(namespace, service string, newDef []byte, updatePeriod time.Duration) error

	// Automate turns on automatic releases for the given service.
	// Read the history for the service to check status.
	Automate(namespace, service string) error

	// Deautomate turns off automatic releases for the given service.
	// Read the history for the service to check status.
	Deautomate(namespace, service string) error
}

var (
	// ErrNoPlatformConfigured indicates a service was constructed without a
	// reference to a runtime platform. A programmer or configuration error.
	ErrNoPlatformConfigured = errors.New("no platform configured")
)

// NewService returns a service connected to the provided Kubernetes platform.
func NewService(reg *registry.Client, k8s *kubernetes.Cluster, auto *automator.Automator, history history.DB) Service {
	return &service{
		registry:  reg,
		platform:  k8s,
		automator: auto,
		history:   history,
	}
}

type service struct {
	registry  *registry.Client
	platform  *kubernetes.Cluster // TODO(pb): replace with platform.Platform when we have that
	automator *automator.Automator
	history   history.DB
}

// ContainerImages describes a combination of a platform container spec, and the
// available images in the corresponding registry.
type ContainerImages struct {
	Container platform.Container
	Images    []registry.Image
}

func (s *service) Images(repository string) ([]registry.Image, error) {
	repo, err := s.registry.GetRepository(repository)
	if err != nil {
		return nil, err
	}
	return repo.Images, nil
}

func (s *service) ServiceImages(namespace, service string) ([]ContainerImages, error) {
	containers, err := s.platform.ContainersFor(namespace, service)
	if err != nil {
		return nil, err
	}
	var result []ContainerImages
	for _, container := range containers {
		repository, err := s.registry.GetRepository(registry.ParseImage(container.Image).Repository())
		if err != nil {
			return nil, err
		}
		result = append(result, ContainerImages{container, repository.Images})
	}
	return result, nil
}

func (s *service) Services(namespace string) ([]platform.Service, error) {
	if s.platform == nil {
		return nil, ErrNoPlatformConfigured
	}
	return s.platform.Services(namespace)
}

func (s *service) History(namespace, service string) (map[string]history.History, error) {
	if service == "" {
		return s.history.AllEvents(namespace)
	}

	h, err := s.history.EventsForService(namespace, service)
	if err == history.ErrNoHistory {
		// TODO(pb): not super happy with this
		h = history.History{
			Service: service,
			State:   history.StateUnknown,
		}
	} else if err != nil {
		return nil, err
	}

	return map[string]history.History{
		h.Service: h,
	}, nil
}

func (s *service) Release(namespace, service string, newDef []byte, updatePeriod time.Duration) (err error) {
	if s.platform == nil {
		return ErrNoPlatformConfigured
	}
	s.history.ChangeState(namespace, service, history.StateInProgress)
	defer func() {
		if err != nil {
			s.history.LogEvent(namespace, service, "Release failed: "+err.Error())
		} else {
			s.history.LogEvent(namespace, service, "Release succeeded")
		}
		s.history.ChangeState(namespace, service, history.StateRest)
	}()
	return s.platform.Release(namespace, service, newDef, updatePeriod)
}

func (s *service) Automate(namespace, service string) error {
	s.automator.Enable(namespace, service)
	return nil
}

func (s *service) Deautomate(namespace, service string) error {
	s.automator.Disable(namespace, service)
	return nil
}