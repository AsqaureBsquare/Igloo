import os
import uuid
import logging
import boto3
from typing import List, Optional
from models import Workspace, WorkspaceStatus, WorkspaceType
from providers.base import WorkspaceProvider

logger = logging.getLogger(__name__)

# Workspace type → (cpu_units, memory_mb, volume_gb)
WORKSPACE_SPECS = {
    WorkspaceType.CONTAINER: (512,  1024, 20),
    WorkspaceType.VM:        (1024, 2048, 50),
    WorkspaceType.GPU:       (4096, 8192, 100),
}


class AWSProvider(WorkspaceProvider):
    def __init__(self):
        self.region                = os.environ["AWS_REGION"]
        self.cluster_name          = os.environ["ECS_CLUSTER"]
        self.task_def_family       = os.environ["TASK_DEFINITION"]
        self.workspace_sg_id       = os.environ["WORKSPACE_SG_ID"]
        self.fargate_task_role_arn      = os.environ["FARGATE_TASK_ROLE_ARN"]
        self.ecs_infrastructure_role_arn = os.environ["ECS_INFRASTRUCTURE_ROLE_ARN"]
        self.private_subnet_ids: List[str] = os.environ["PRIVATE_SUBNET_IDS"].split(",")

        self._ecs = boto3.client("ecs", region_name=self.region)
        self._ec2 = boto3.client("ec2",  region_name=self.region)

    # ─── helpers ──────────────────────────────────────────────────────────────

    def _run_task(self, subnet_id: str, volume_config: dict, workspace_id: str, user_id: str) -> str:
        """Launch a Fargate task with the given EBS volume configuration."""
        resp = self._ecs.run_task(
            cluster=self.cluster_name,
            taskDefinition=self.task_def_family,
            launchType="FARGATE",
            enableExecuteCommand=True,
            propagateTags="TASK_DEFINITION",
            networkConfiguration={
                "awsvpcConfiguration": {
                    "subnets":        [subnet_id],
                    "securityGroups": [self.workspace_sg_id],
                    "assignPublicIp": "DISABLED",
                }
            },
            volumeConfigurations=[{
                "name": "workspace-data",
                "managedEBSVolume": volume_config,
            }],
            tags=[
                {"key": "igloo:workspace_id", "value": workspace_id},
                {"key": "igloo:user_id",      "value": user_id},
            ],
        )

        failures = resp.get("failures", [])
        if failures:
            raise RuntimeError(f"ECS RunTask failed: {failures}")

        return resp["tasks"][0]["taskArn"]

    # ─── interface ────────────────────────────────────────────────────────────

    def create_workspace(
        self,
        user_id: str,
        workspace_type: WorkspaceType = WorkspaceType.CONTAINER,
        template: str = "default",
    ) -> Workspace:
        workspace_id = str(uuid.uuid4())
        _, _, volume_gb = WORKSPACE_SPECS[workspace_type]

        subnet_id = self.private_subnet_ids[0]

        # Let Fargate create and attach a brand-new EBS volume.
        # deleteOnTermination=False ensures the volume persists after task stops.
        # Volume ID will be extracted by workspace_status_sync via DescribeTasks.
        volume_config = {
            "roleArn":          self.ecs_infrastructure_role_arn,
            "encrypted":        True,
            "volumeType":       "gp3",
            "sizeInGiB":        volume_gb,
            "filesystemType":   "ext4",
            "terminationPolicy": {"deleteOnTermination": False},
            "tagSpecifications": [{
                "resourceType": "volume",
                "tags": [
                    {"key": "Name",               "value": f"igloo-{workspace_id}"},
                    {"key": "igloo:workspace_id", "value": workspace_id},
                    {"key": "igloo:user_id",      "value": user_id},
                ],
            }],
        }

        task_arn = self._run_task(subnet_id, volume_config, workspace_id, user_id)
        logger.info("Started Fargate task %s for new workspace %s", task_arn, workspace_id)

        return Workspace(
            workspace_id   = workspace_id,
            user_id        = user_id,
            status         = WorkspaceStatus.CREATING,
            workspace_type = workspace_type,
            task_arn       = task_arn,
            subnet_id      = subnet_id,
            template       = template,
        )

    def sleep_workspace(self, workspace: Workspace) -> None:
        if not workspace.task_arn:
            logger.warning("sleep called but no task_arn for %s", workspace.workspace_id)
            return

        self._ecs.stop_task(
            cluster=self.cluster_name,
            task=workspace.task_arn,
            reason="igloo: auto-sleep",
        )
        logger.info("Stopped task %s for workspace %s", workspace.task_arn, workspace.workspace_id)

    def wake_workspace(self, workspace: Workspace) -> str:
        if not workspace.volume_id:
            raise ValueError(f"No volume_id for workspace {workspace.workspace_id}")

        # Wait until EBS volume is detached from the stopped task
        waiter = self._ec2.get_waiter("volume_available")
        waiter.wait(VolumeIds=[workspace.volume_id])

        # Re-attach the existing volume by volumeId.
        # Requires latest boto3 (bundled in Lambda layer, not runtime default).
        volume_config = {
            "volumeId":          workspace.volume_id,
            "roleArn":           self.ecs_infrastructure_role_arn,
            "terminationPolicy": {"deleteOnTermination": False},
        }

        subnet_id = workspace.subnet_id or self.private_subnet_ids[0]
        task_arn  = self._run_task(subnet_id, volume_config, workspace.workspace_id, workspace.user_id)
        logger.info("Woke workspace %s with task %s (volume %s)", workspace.workspace_id, task_arn, workspace.volume_id)
        return task_arn

    def destroy_workspace(self, workspace: Workspace, keep_volume: bool = False) -> None:
        if workspace.task_arn and workspace.status == WorkspaceStatus.RUNNING:
            try:
                self._ecs.stop_task(
                    cluster=self.cluster_name,
                    task=workspace.task_arn,
                    reason="igloo: workspace destroyed",
                )
            except Exception as e:
                logger.warning("Could not stop task %s: %s", workspace.task_arn, e)

        if workspace.volume_id and not keep_volume:
            try:
                waiter = self._ec2.get_waiter("volume_available")
                waiter.wait(VolumeIds=[workspace.volume_id])
                self._ec2.delete_volume(VolumeId=workspace.volume_id)
                logger.info("Deleted volume %s", workspace.volume_id)
            except Exception as e:
                logger.warning("Could not delete volume %s: %s", workspace.volume_id, e)

    def get_connection(self, workspace: Workspace) -> dict:
        import time
        from botocore.exceptions import ClientError

        if workspace.status != WorkspaceStatus.RUNNING:
            raise ValueError(f"Workspace {workspace.workspace_id} is not running")

        task_id = workspace.task_arn.split("/")[-1]

        # SSM may not be ready immediately after ECS reports the agent RUNNING.
        # Retry for up to 30s on TargetNotConnected.
        for attempt in range(6):
            try:
                resp = self._ecs.execute_command(
                    cluster=self.cluster_name,
                    task=task_id,
                    container="workspace",
                    command="/bin/bash",
                    interactive=True,
                )
                break
            except Exception as e:
                err = str(e)
                if ("TargetNotConnected" in err or "execute command agent" in err.lower()) and attempt < 5:
                    logger.info("ECS Exec not ready yet (attempt %d/6), retrying in 5s...", attempt + 1)
                    time.sleep(5)
                else:
                    raise

        session = resp["session"]
        return {
            "type":        "ecs_exec",
            "cluster":     self.cluster_name,
            "task_arn":    workspace.task_arn,
            "session_id":  session["sessionId"],
            "stream_url":  session["streamUrl"],
            "token_value": session["tokenValue"],
            "region":      self.region,
        }

    def get_status(self, workspace: Workspace) -> WorkspaceStatus:
        if not workspace.task_arn:
            return WorkspaceStatus.SLEEPING

        task_id = workspace.task_arn.split("/")[-1]
        resp    = self._ecs.describe_tasks(cluster=self.cluster_name, tasks=[task_id])
        tasks   = resp.get("tasks", [])

        if not tasks:
            return WorkspaceStatus.SLEEPING

        mapping = {
            "PROVISIONING":   WorkspaceStatus.CREATING,
            "PENDING":        WorkspaceStatus.CREATING,
            "ACTIVATING":     WorkspaceStatus.CREATING,
            "RUNNING":        WorkspaceStatus.RUNNING,
            "DEACTIVATING":   WorkspaceStatus.SLEEPING,
            "STOPPING":       WorkspaceStatus.SLEEPING,
            "DEPROVISIONING": WorkspaceStatus.SLEEPING,
            "STOPPED":        WorkspaceStatus.SLEEPING,
        }
        return mapping.get(tasks[0].get("lastStatus", "STOPPED"), WorkspaceStatus.ERROR)
