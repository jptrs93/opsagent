type ScheduledInstance@2 root {
    id@1: u64 [key]
    node_id@2: Node.id [mutable = false]
    instance_ordinal@3: u32 [mutable = false]
    state@4: enum ScheduledInstanceTarget {
        RunServing@0
        Terminate@1
        Finalized@2
        RunStandby@3
        RunDraining@4
        transitions {
            start -> RunServing | RunStandby
            RunStandby -> RunServing | Terminate | Finalized
            RunServing -> RunDraining | Terminate | Finalized
            RunDraining -> Terminate | Finalized
            Terminate -> Finalized
        }
    }
    space_id@5: Space.id [mutable = false]
    deployment@6: type DeploymentRef {
        deployment_id@1: Deployment.id
        version@2: u32 [min = 1]
    } [mutable = false]
    laws {
        serving_slot where state == RunServing {
            unique (deployment.deployment_id, instance_ordinal)
        }
    }
}

type ScheduledInstanceStatus@16 root {
    scheduled_instance_id@1: ScheduledInstance.id [key]
    updated_at@2: i64
    preparer@3: ?type PreparerStatus {
        deployment_spec_version@1: u32
        artifact@2: string
        inputs@3: enum InputsStatus {
            Resolving@1
            Ready@2
            Failed@3
        }
        image@4: ?enum ImageStatus {
            Building@1
            Pulling@2
            Downloading@3
            Ready@4
            Failed@5
        }
    }
    runner@4: ?type RunnerStatus {
        deployment_spec_version@1: u32
        running_pid@2: ?u32 [min = 1]
        running_artifact@3: string
        status@4: enum RunningStatus {
            NoDeployment@1
            Running@2
            Stopped@3
            Starting@4
            Crashed@5
        }
        number_of_restarts@5: u32
        last_restart_at@6: ?i64
        network_diagnostics@7: []string
        exit_code@8: ?i32
    }
    laws {
        clock_monotonic on update {
            assert after.updated_at >= before.updated_at
        }
    }
}
