# Quick Start

## CRDs

**TofuProgram** defines the infrastructure code — either inline HCL or a git repository source, plus provider requirements.

**TofuProject** binds a program to a specific environment — parameters, backend config, and execution settings (autoApprove, suspend, syncInterval, cache, dependencies).

## Usage

Reference a git repository (recommended):

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: aws-vpc
spec:
  source:
    url: https://github.com/acme-corp/infrastructure.git
    ref: main
    path: modules/vpc
  providers:
    - name: aws
      source: "hashicorp/aws"
      version: "~> 5.0"
      configHCL: |
        region = var.aws_region
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: staging-vpc
spec:
  programRef:
    name: aws-vpc
  params:
    aws_region: "eu-central-1"
    vpc_cidr: "10.1.0.0/16"
    environment: "staging"
  autoApprove: false
  syncInterval: "1h"
```

Or use inline HCL for simpler resources:

```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: s3-bucket
spec:
  providers:
    - name: aws
      source: "hashicorp/aws"
      version: "~> 5.0"
      configHCL: |
        region = var.aws_region
  programHCL: |
    variable "aws_region" { type = string }
    variable "bucket_name" { type = string }
    variable "environment" { type = string }

    resource "aws_s3_bucket" "this" {
      bucket = var.bucket_name
      tags = {
        Environment = var.environment
        ManagedBy   = "tofu-k8s-operator"
      }
    }

    resource "aws_s3_bucket_versioning" "this" {
      bucket = aws_s3_bucket.this.id
      versioning_configuration {
        status = "Enabled"
      }
    }

    output "bucket_arn" {
      value = aws_s3_bucket.this.arn
    }
---
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: logs-bucket
spec:
  programRef:
    name: s3-bucket
  params:
    aws_region: "eu-central-1"
    bucket_name: "acme-staging-logs"
    environment: "staging"
  autoApprove: true
```
