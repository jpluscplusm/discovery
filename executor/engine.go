package main

import (
	"context"
	"fmt"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"log"
	"syscall"
)

type TaskExitCode struct {
	GroupName  string
	TaskGUID   string
	TaskRemote string
	ExitCode   containerd.ExitStatus
}

type Executor struct {
	Namespace     string
	Client        *containerd.Client
	Ctx           context.Context
	Groups        map[string]Group
	Tasks         map[string]containerd.Task
	NewGroups     <-chan Group
	DeleteGroups  <-chan string
	TaskExitCodes chan TaskExitCode
}

func NewExecutor(namespace string, client *containerd.Client, newGroups <-chan Group, deleteGroups <-chan string) *Executor {
	ctx := namespaces.WithNamespace(context.Background(), namespace)
	taskExitCodes := make(chan TaskExitCode)
	return &Executor{
		Namespace:     namespace,
		Client:        client,
		Ctx:           ctx,
		Groups:        map[string]Group{},
		Tasks:         map[string]containerd.Task{},
		NewGroups:     newGroups,
		DeleteGroups:  deleteGroups,
		TaskExitCodes: taskExitCodes,
	}
}

func (e *Executor) createGroup(group Group) {
	e.Groups[group.Name] = group
	for _, machine := range group.Machines {
		e.createTask(group.Name, machine)
	}
}

func (e *Executor) createTask(groupName string, machine Machine) {
	task, err := runTask(machine, "discovery-"+groupName, e.Client)
	if err != nil {
		log.Fatalln(fmt.Errorf("Error running task of %s (%s): %s", machine.GUID, machine.Remote, err))
	}
	exitStatusC, err := task.Wait(e.Ctx)
	if err != nil {
		log.Fatalln(fmt.Errorf("Error waiting for task %s (%s): %s", machine.GUID, machine.Remote, err))
	}
	if err := task.Start(e.Ctx); err != nil {
		log.Fatalln(fmt.Errorf("Error starting task %s (%s): %s", machine.GUID, machine.Remote, err))
	}
	e.Tasks[machine.Remote] = task
	go func(taskExitCodes chan TaskExitCode, exitStatusC <-chan containerd.ExitStatus) {
		exitStatus := <-exitStatusC
		taskExitCodes <- TaskExitCode{
			GroupName:  groupName,
			TaskGUID:   machine.GUID,
			TaskRemote: machine.Remote,
			ExitCode:   exitStatus,
		}
	}(e.TaskExitCodes, exitStatusC)
}

func (e *Executor) deleteGroup(groupName string) {
	for remote := range e.Groups[groupName].Machines {
		prevStatus, err := e.Tasks[remote].Status(e.Ctx)
		if err != nil {
			log.Fatalln(fmt.Errorf("Error getting status for %s when deleting group %s: %s", remote, groupName, err))
		}

		// @TODO: Potential race condition with restarting tasks, in time.
		// @TODO: Check about paused/pausing containers.
		// @TODO: Want to SIGTERM but then SIGKILL if not stopped within given time.
		if prevStatus.Status == containerd.Running || prevStatus.Status == containerd.Paused || prevStatus.Status == containerd.Pausing {
			err = e.Tasks[remote].Kill(e.Ctx, syscall.SIGTERM)
			if err != nil {
				log.Fatalln(fmt.Errorf("Error deleting group %s (%s): %s", groupName, prevStatus, err))
			}
		}
	}
	// @TODO: May want to retain for awhile to ensure all tasks are gone.
	delete(e.Groups, groupName)
}

func (e *Executor) run() {
	for {
		select {
		case newGroup := <-e.NewGroups:
			e.createGroup(newGroup)
		case groupName := <-e.DeleteGroups:
			e.deleteGroup(groupName)
		case taskExitCode := <-e.TaskExitCodes:
			log.Printf("%+v\n", taskExitCode)

			err := e.Tasks[taskExitCode.TaskRemote].Kill(e.Ctx, syscall.SIGTERM, containerd.WithKillAll)
			if err != nil && !errdefs.IsFailedPrecondition(err) && !errdefs.IsNotFound(err) {
				log.Fatalln(fmt.Errorf("Error killing task (%s, %s): %s", taskExitCode.TaskGUID, taskExitCode.TaskRemote, err))
			}

			_, err = e.Tasks[taskExitCode.TaskRemote].Delete(e.Ctx)
			if err != nil {
				log.Fatalln(fmt.Errorf("Error deleting task (%s, %s): %s", taskExitCode.TaskGUID, taskExitCode.TaskRemote, err))
			}

			exitStatusC, err := e.Tasks[taskExitCode.TaskRemote].Wait(e.Ctx)
			if err != nil {
				log.Fatalln(fmt.Errorf("Error waiting for task %s (%s): %s", taskExitCode.TaskGUID, taskExitCode.TaskRemote, err))
			}
			<-exitStatusC

			container, err := e.Client.LoadContainer(e.Ctx, taskExitCode.TaskGUID)
			if err != nil {
				log.Fatalln(fmt.Errorf("Error loading container %s (%s): %s", taskExitCode.TaskGUID, taskExitCode.TaskRemote, err))
			}

			err = container.Delete(e.Ctx, containerd.WithSnapshotCleanup)
			if err != nil {
				log.Println(fmt.Errorf("Error deleting container %s (%s): %s", taskExitCode.TaskGUID, taskExitCode.TaskRemote, err))
			}

			if _, ok := e.Groups[taskExitCode.GroupName]; ok {
				e.createTask(taskExitCode.GroupName, e.Groups[taskExitCode.GroupName].Machines[taskExitCode.TaskRemote])
			}
		}
	}
}
