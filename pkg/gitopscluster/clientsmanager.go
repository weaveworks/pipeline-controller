package gitopscluster

import (
	"errors"

	"k8s.io/client-go/rest"
)

var (
	ErrClientNotFound = errors.New("cluster client not found")
)

type RestConfigManager interface {
	Get(name string, namespace string) (*rest.Config, error)
	Add(name string, namespace string, client *rest.Config) error
	Delete(name string, namespace string) error

	AddEventHandler(eventHandler EventHandlerFuncs)
}

type EventHandlerFuncs struct {
	AddFunc    func(name string, namespace string, client *rest.Config)
	DeleteFunc func(name string, namespace string)
}

type clientsManager struct {
	restConfigs   map[string]*rest.Config
	eventHandlers []EventHandlerFuncs
}

func NewClientsManager() RestConfigManager {
	return &clientsManager{
		restConfigs: make(map[string]*rest.Config),
	}
}

func (c *clientsManager) Get(name string, namespace string) (*rest.Config, error) {
	client, ok := c.restConfigs[ClusterKey(name, namespace)]
	if !ok {
		return nil, ErrClientNotFound
	}

	return client, nil
}

func (c *clientsManager) Add(name string, namespace string, client *rest.Config) error {
	c.restConfigs[ClusterKey(name, namespace)] = client

	for _, eventHandler := range c.eventHandlers {
		if eventHandler.AddFunc != nil {
			eventHandler.AddFunc(name, namespace, client)
		}
	}

	return nil
}

func (c *clientsManager) Delete(name string, namespace string) error {
	for _, eventHandler := range c.eventHandlers {
		if eventHandler.DeleteFunc != nil {
			eventHandler.DeleteFunc(name, namespace)
		}
	}

	delete(c.restConfigs, ClusterKey(name, namespace))

	return nil
}

func (c *clientsManager) AddEventHandler(eventHandler EventHandlerFuncs) {
	c.eventHandlers = append(c.eventHandlers, eventHandler)
}

func ClusterKey(name string, namespace string) string {
	return name + "/" + namespace
}
