package deployer

import (
	"fmt"
	"github.com/46bit/discovery/deployer/runtime"
)

// deployment:
//   name: "senders-receiver"
//   jobs:
//   - name: "sender"
//     remote: "docker.io/46bit/sender:latest"
//     instances: 3
//   - name: "receiver"
//     remote: "docker.io/46bit/receiver:latest"
//     instances: 1

type Deployment struct {
	Name string `json:"name"`
	Jobs []Job  `json:"jobs"`
}

func (d *Deployment) Namespace() string {
	return "deployment." + d.Name
}

type Job struct {
	Name      string `json:"name"`
	Remote    string `json:"remote"`
	Instances uint   `json:"instances"`
}

func (j *Job) ContainerID(instanceNumber uint) string {
	return fmt.Sprintf("%s.%d", j.Name, instanceNumber)
}

func (j *Job) Containers(namespace string) []runtime.Container {
	containers := []runtime.Container{}
	for i := uint(0); i < j.Instances; i++ {
		containers = append(containers, runtime.Container{
			ID:        j.ContainerID(i),
			Remote:    j.Remote,
			Namespace: namespace,
		})
	}
	return containers
}
