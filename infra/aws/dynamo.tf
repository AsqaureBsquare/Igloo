resource "aws_dynamodb_table" "igloo_users" {
  name         = "igloo-users"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  tags = { Project = "igloo" }
}

resource "aws_dynamodb_table" "igloo_workspaces" {
  name         = "igloo-workspaces"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  tags = { Project = "igloo" }
}

resource "aws_dynamodb_table" "igloo_templates" {
  name         = "igloo-templates"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  tags = { Project = "igloo" }
}

resource "aws_dynamodb_table" "igloo_ws_jobs" {
  name         = "igloo-ws-jobs"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "workspaceId"
  range_key    = "id"

  attribute {
    name = "workspaceId"
    type = "S"
  }

  attribute {
    name = "id"
    type = "S"
  }

  tags = { Project = "igloo" }
}

resource "aws_dynamodb_table" "igloo_provisioner_keys" {
  name         = "igloo-provisioner-keys"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  tags = { Project = "igloo" }
}

resource "aws_dynamodb_table" "igloo_entities" {
  name         = "igloo-entities"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  tags = { Project = "igloo" }
}
