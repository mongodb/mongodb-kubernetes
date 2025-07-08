# ClusterServiceVersion Image Backup Script

This Python script backs up all digest-pinned container images from a ClusterServiceVersion (CSV) file to Amazon ECR with a timestamped tag suffix.

## Overview

The script performs the following operations:

1. **Parses a ClusterServiceVersion YAML file** to extract container images from:
   - Deployment container specifications
   - `RELATED_IMAGE_*` environment variables
   - `relatedImages` section
   - `containerImage` annotation

2. **Identifies digest-pinned images** (those containing `@sha256:`)

3. **Backs up images to ECR** with the format:
   ```
   268558157000.dkr.ecr.us-east-1.amazonaws.com/backup/<version>/<image-name>:<original-tag>
   ```

## Prerequisites

- Python 3.6+
- Docker CLI
- AWS CLI configured with appropriate permissions
- PyYAML library (`pip install PyYAML`)

### Required AWS Permissions

Your AWS credentials need the following ECR permissions:
- `ecr:GetAuthorizationToken`
- `ecr:CreateRepository`
- `ecr:DescribeRepositories`
- `ecr:BatchCheckLayerAvailability`
- `ecr:GetDownloadUrlForLayer`
- `ecr:BatchGetImage`
- `ecr:PutImage`
- `ecr:InitiateLayerUpload`
- `ecr:UploadLayerPart`
- `ecr:CompleteLayerUpload`

## Installation

1. Clone or download the script files:
   ```bash
   # Download the main script
   curl -O https://raw.githubusercontent.com/your-repo/backup_csv_images.py

   # Install dependencies
   pip install PyYAML
   ```

2. Make the script executable:
   ```bash
   chmod +x backup_csv_images.py
   ```

## Usage

### Basic Usage

```bash
# Backup all digest-pinned images from a CSV file
python backup_csv_images.py /path/to/clusterserviceversion.yaml
```

### Dry Run Mode

```bash
# See what would be backed up without actually doing it
python backup_csv_images.py --dry-run /path/to/clusterserviceversion.yaml
```



### Verbose Output

```bash
# Enable detailed logging
python backup_csv_images.py --verbose /path/to/clusterserviceversion.yaml
```

### Command Line Options

- `csv_file`: Path to the ClusterServiceVersion YAML file (required)
- `--dry-run`: Show what would be backed up without executing

- `--verbose, -v`: Enable verbose logging
- `--version`: Version to use in backup path (default: extracted from CSV metadata)
- `--help, -h`: Show help message

## Examples

### Example 1: Basic Backup

```bash
python backup_csv_images.py bundle/1.1.1/manifests/mongodb-kubernetes.clusterserviceversion.yaml
```

Output:
```
2025-01-07 12:00:00,123 - INFO - Using timestamp: 20250107_120000
2025-01-07 12:00:00,456 - INFO - Successfully parsed CSV file: bundle/1.1.1/manifests/mongodb-kubernetes.clusterserviceversion.yaml
2025-01-07 12:00:00,789 - INFO - Extracted 150 total images from CSV
2025-01-07 12:00:01,012 - INFO - Found 75 digest-pinned images
2025-01-07 12:00:01,234 - INFO - Backup plan for 75 images:
2025-01-07 12:00:01,345 - INFO -   quay.io/mongodb/mongodb-agent-ubi@sha256:abc123... -> 268558157000.dkr.ecr.us-east-1.amazonaws.com/backup/1.2.0/mongodb-agent-ubi:107.0.13.8702-1
...
```

### Example 2: Dry Run

```bash
python backup_csv_images.py --dry-run bundle/1.1.1/manifests/mongodb-kubernetes.clusterserviceversion.yaml
```

This will show the backup plan without actually pulling, tagging, or pushing any images.





## How It Works

### Image Extraction

The script extracts images from multiple locations in the CSV:

1. **Deployment containers**: `spec.install.spec.deployments[].spec.template.spec.containers[].image`
2. **Environment variables**: Any env var starting with `RELATED_IMAGE_`
3. **Related images**: `spec.relatedImages[].image`
4. **Container annotation**: `metadata.annotations.containerImage`

### Digest Detection

Only images containing `@sha256:` are considered digest-pinned and will be backed up.

### Backup Process

For each digest-pinned image:

1. **ECR Repository Creation**: Creates the ECR repository if it doesn't exist
2. **Image Pull**: Pulls the original image using its digest
3. **Image Tagging**: Tags the image with the backup name
4. **Image Push**: Pushes to ECR
5. **Cleanup**: Removes the local backup tag

### Tag Generation

The backup tag format preserves the original tag: `<original-tag>`

- For digest-pinned images without explicit tags, uses `latest` as the tag
- Extracts the repository name from the full image path
- All images are stored under the `backup/<version>/` prefix in ECR

## Testing

Run the test script to verify functionality:

```bash
python test_backup_script.py
```

This will run unit tests for:
- Image URL parsing
- Backup tag generation
- CSV parsing and image extraction

## Troubleshooting

### Common Issues

1. **AWS Authentication Errors**
   ```
   Failed to login to ECR: ...
   ```
   - Ensure AWS CLI is configured: `aws configure`
   - Check your AWS credentials have ECR permissions

2. **Docker Not Running**
   ```
   Cannot connect to the Docker daemon
   ```
   - Start Docker: `sudo systemctl start docker` (Linux) or start Docker Desktop

3. **Image Pull Failures**
   ```
   Failed to pull image: ...
   ```
   - Check if the source registry requires authentication
   - Verify the image digest is correct and the image exists

4. **ECR Repository Creation Failures**
   ```
   Failed to create ECR repository: ...
   ```
   - Check ECR permissions
   - Verify the repository name is valid (lowercase, alphanumeric, hyphens, underscores, periods)

### Debug Mode

Use `--verbose` flag for detailed logging:

```bash
python backup_csv_images.py --verbose --dry-run /path/to/csv.yaml
```

## Security Considerations

- The script requires Docker and AWS CLI access
- ECR repositories will be created automatically if they don't exist
- Images are temporarily stored locally during the backup process
- Consider running in a secure environment with appropriate network access controls

## License

This script is provided as-is for backing up container images from ClusterServiceVersion files.
