locals {
  src_root = "${path.root}/../../"
  runtime  = "python3.12"
  timeout  = 300

  env = {
    WORKSPACES_TABLE      = var.workspaces_table_name
    ECS_CLUSTER           = var.ecs_cluster_name
    TASK_DEFINITION       = var.task_definition_family
    WORKSPACE_SG_ID       = var.workspace_sg_id
    PRIVATE_SUBNET_IDS    = join(",", var.private_subnet_ids)
    FARGATE_TASK_ROLE_ARN      = var.fargate_task_role_arn
    ECS_INFRASTRUCTURE_ROLE_ARN = var.ecs_infrastructure_role_arn
    IDLE_TIMEOUT_MINUTES  = tostring(var.idle_timeout_minutes)
  }
}

# ─── Lambda Layer — core code + latest boto3 ─────────────────────────────────
# Uses pip install to get the latest boto3 (Lambda runtime ships an old version
# that lacks newer ECS/EC2 API parameters like managedEBSVolume.volumeId).

locals {
  layer_build_dir = "${path.module}/tmp/layer_build"
  layer_zip       = "${path.module}/tmp/layer.zip"

  # Trigger rebuild when any core source file changes
  layer_src_hash = sha256(join("", [
    filesha256("${local.src_root}core/models.py"),
    filesha256("${local.src_root}core/db.py"),
    filesha256("${local.src_root}core/providers/__init__.py"),
    filesha256("${local.src_root}core/providers/base.py"),
    filesha256("${local.src_root}core/providers/aws.py"),
  ]))
}

resource "null_resource" "build_layer" {
  triggers = {
    src_hash = local.layer_src_hash
  }

  provisioner "local-exec" {
    command = <<-EOF
      set -e
      rm -rf "${local.layer_build_dir}"
      mkdir -p "${local.layer_build_dir}/python"
      pip3 install boto3 --quiet --upgrade -t "${local.layer_build_dir}/python"
      cp "${local.src_root}core/models.py"              "${local.layer_build_dir}/python/models.py"
      cp "${local.src_root}core/db.py"                  "${local.layer_build_dir}/python/db.py"
      mkdir -p "${local.layer_build_dir}/python/providers"
      cp "${local.src_root}core/providers/__init__.py"  "${local.layer_build_dir}/python/providers/__init__.py"
      cp "${local.src_root}core/providers/base.py"      "${local.layer_build_dir}/python/providers/base.py"
      cp "${local.src_root}core/providers/aws.py"       "${local.layer_build_dir}/python/providers/aws.py"
    EOF
  }
}

data "archive_file" "layer" {
  type        = "zip"
  source_dir  = local.layer_build_dir
  output_path = local.layer_zip
  depends_on  = [null_resource.build_layer]
}

resource "aws_lambda_layer_version" "core" {
  layer_name               = "${var.project}-core"
  filename                 = data.archive_file.layer.output_path
  source_code_hash         = data.archive_file.layer.output_base64sha256
  compatible_runtimes      = [local.runtime]
  compatible_architectures = ["arm64"]
  depends_on               = [null_resource.build_layer]
}

# ─── Helper: one Lambda function ─────────────────────────────────────────────

locals {
  functions = {
    workspace_create  = { handler_path = "${local.src_root}api/workspace_create/handler.py",  method = "POST" }
    workspace_sleep   = { handler_path = "${local.src_root}api/workspace_sleep/handler.py",   method = "POST" }
    workspace_wake    = { handler_path = "${local.src_root}api/workspace_wake/handler.py",    method = "POST" }
    workspace_destroy = { handler_path = "${local.src_root}api/workspace_destroy/handler.py", method = "DELETE" }
    workspace_connect = { handler_path = "${local.src_root}api/workspace_connect/handler.py", method = "GET" }
    idle_monitor      = { handler_path = "${local.src_root}api/idle_monitor/handler.py",      method = null }
  }
}

data "archive_file" "functions" {
  for_each    = local.functions
  type        = "zip"
  output_path = "${path.module}/tmp/${each.key}.zip"

  source {
    content  = file(each.value.handler_path)
    filename = "handler.py"
  }
}

resource "aws_lambda_function" "functions" {
  for_each = local.functions

  function_name    = "${var.project}-${each.key}"
  role             = var.lambda_role_arn
  runtime          = local.runtime
  architectures    = ["arm64"]
  handler          = "handler.lambda_handler"
  filename         = data.archive_file.functions[each.key].output_path
  source_code_hash = data.archive_file.functions[each.key].output_base64sha256
  timeout          = local.timeout
  memory_size      = 256
  layers           = [aws_lambda_layer_version.core.arn]

  environment {
    variables = local.env
  }

  depends_on = [aws_cloudwatch_log_group.functions]
}

resource "aws_cloudwatch_log_group" "functions" {
  for_each          = local.functions
  name              = "/aws/lambda/${var.project}-${each.key}"
  retention_in_days = 14
}

# ─── workspace_status_sync — separate from the for_each map (different trigger) ──

data "archive_file" "status_sync" {
  type        = "zip"
  output_path = "${path.module}/tmp/workspace_status_sync.zip"
  source {
    content  = file("${local.src_root}api/workspace_status_sync/handler.py")
    filename = "handler.py"
  }
}

resource "aws_cloudwatch_log_group" "status_sync" {
  name              = "/aws/lambda/${var.project}-workspace_status_sync"
  retention_in_days = 14
}

resource "aws_lambda_function" "status_sync" {
  function_name    = "${var.project}-workspace_status_sync"
  role             = var.lambda_role_arn
  runtime          = local.runtime
  architectures    = ["arm64"]
  handler          = "handler.lambda_handler"
  filename         = data.archive_file.status_sync.output_path
  source_code_hash = data.archive_file.status_sync.output_base64sha256
  timeout          = 30
  memory_size      = 128
  layers           = [aws_lambda_layer_version.core.arn]

  environment {
    variables = local.env
  }

  depends_on = [aws_cloudwatch_log_group.status_sync]
}

resource "aws_cloudwatch_event_rule" "ecs_task_change" {
  name        = "${var.project}-ecs-task-state-change"
  description = "ECS task state changes → update workspace status in DynamoDB"

  event_pattern = jsonencode({
    source      = ["aws.ecs"]
    "detail-type" = ["ECS Task State Change"]
    detail = {
      clusterArn = [var.ecs_cluster_arn]
    }
  })
}

resource "aws_cloudwatch_event_target" "status_sync" {
  rule      = aws_cloudwatch_event_rule.ecs_task_change.name
  target_id = "workspace-status-sync"
  arn       = aws_lambda_function.status_sync.arn
}

resource "aws_lambda_permission" "status_sync" {
  statement_id  = "AllowEventBridgeStatusSync"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.status_sync.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.ecs_task_change.arn
}

# ─── EventBridge — idle_monitor schedule ─────────────────────────────────────

resource "aws_cloudwatch_event_rule" "idle_monitor" {
  name                = "${var.project}-idle-monitor"
  description         = "Trigger idle workspace monitor every 5 minutes"
  schedule_expression = "rate(5 minutes)"
}

resource "aws_cloudwatch_event_target" "idle_monitor" {
  rule      = aws_cloudwatch_event_rule.idle_monitor.name
  target_id = "idle-monitor-lambda"
  arn       = aws_lambda_function.functions["idle_monitor"].arn
}

resource "aws_lambda_permission" "idle_monitor" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.functions["idle_monitor"].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.idle_monitor.arn
}
