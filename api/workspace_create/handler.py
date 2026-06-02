import json
import logging
from models import WorkspaceType
from providers.aws import AWSProvider
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

provider = AWSProvider()


def lambda_handler(event, context):
    try:
        body = json.loads(event.get("body") or "{}")

        user_id        = event["requestContext"]["authorizer"]["user_id"]
        workspace_type = WorkspaceType(body.get("type", WorkspaceType.CONTAINER.value))
        template       = body.get("template", "default")

        workspace = provider.create_workspace(
            user_id        = user_id,
            workspace_type = workspace_type,
            template       = template,
        )

        db.put_workspace(workspace)
        logger.info("Created workspace %s for user %s", workspace.workspace_id, user_id)

        return {
            "statusCode": 201,
            "headers":    {"Content-Type": "application/json"},
            "body":       json.dumps({
                "workspace_id": workspace.workspace_id,
                "status":       workspace.status.value,
                "type":         workspace.workspace_type.value,
                "volume_id":    workspace.volume_id,
                "task_arn":     workspace.task_arn,
            }),
        }

    except ValueError as e:
        return {"statusCode": 400, "body": json.dumps({"error": str(e)})}
    except Exception as e:
        logger.exception("workspace_create failed")
        return {"statusCode": 500, "body": json.dumps({"error": "internal error"})}
