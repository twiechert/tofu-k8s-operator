# Examples

## AWS S3 Bucket

A realistic example using the AWS provider to provision an S3 bucket.

### TofuProgram
```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProgram
metadata:
  name: s3-bucket
  namespace: default
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

    resource "aws_s3_bucket" "bucket" {
      bucket = var.bucket_name
      force_destroy = true
    }

    output "bucket_arn" {
      value = aws_s3_bucket.bucket.arn
    }
```

### TofuProject
```yaml
apiVersion: tofu.example.com/v1alpha1
kind: TofuProject
metadata:
  name: s3-bucket-demo
  namespace: default
spec:
  programRef:
    name: s3-bucket
  params:
    aws_region: "us-west-2"
    bucket_name: "my-demo-bucket-12345"
  backend:
    secretSuffix: "s3-bucket-demo"
    namespace: "default"
  autoApprove: true
```

**Note:**
- You must provide AWS credentials (e.g., via a Kubernetes Secret and projected environment variables) for the operator Job to authenticate with AWS.
- The bucket name must be globally unique.
