import json
import logging
from models import WorkspaceStatus
from providers.aws import AWSProvider
import db

logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)

provider = AWSProvider()


def lambda_handler(event, context):
    try:
        workspace_id = event["pathParameters"]["workspace_id"]
        user_id      = event["requestContext"]["authorizer"]["user_id"]

        workspace = db.get_workspace(workspace_id)
        if not workspace:
            return {"statusCode": 404, "body": json.dumps({"error": "workspace not found"})}

        if workspace.user_id != user_id:
            return {"statusCode": 403, "body": json.dumps({"error": "forbidden"})}

        if workspace.status != WorkspaceStatus.RUNNING:
            return {
                "statusCode": 409,
                "body": json.dumps({"error": f"workspace is {workspace.status.value}, not running"}),
            }

        db.update_workspace(workspace_id, status=WorkspaceStatus.SLEEPING.value)
        provider.sleep_workspace(workspace)

        logger.info("Slept workspace %s", workspace_id)
        return {
            "statusCode": 200,
            "body": json.dumps({"workspace_id": workspace_id, "status": "sleeping"}),
        }

    except Exception as e:
        logger.exception("workspace_sleep failed")
        db.update_workspace(workspace_id, status=WorkspaceStatus.ERROR.value)
        return {"statusCode": 500, "body": json.dumps({"error": "internal error"})}
