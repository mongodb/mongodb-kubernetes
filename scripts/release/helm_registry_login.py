import os
import sys
import subprocess
from lib.base_logger import logger
from scripts.release.build.build_info import load_build_info

def helm_registry_login(helm_registry: str, region: str):
    logger.info(f"Attempting to log into ECR registry: {helm_registry}, using helm registry login.")
    
    aws_command = [
        "aws", 
        "ecr", 
        "get-login-password", 
        "--region", 
        region
    ]
    
    # as we can see the password is being provided by stdin, that would mean we will have to
    # pipe the aws_command (it figures out the password) into helm_command.
    helm_command = [
        "helm", 
        "registry", 
        "login", 
        "--username", 
        "AWS", 
        "--password-stdin", 
        helm_registry
    ]

    try:
        logger.info("Starting AWS ECR credential retrieval.")
        aws_proc = subprocess.Popen(
            aws_command, 
            stdout=subprocess.PIPE, 
            stderr=subprocess.PIPE,
            text=True  # Treat input/output as text strings
        )
        
        logger.info("Starting Helm registry login.")
        helm_proc = subprocess.Popen(
            helm_command, 
            stdin=aws_proc.stdout, 
            stdout=subprocess.PIPE, 
            stderr=subprocess.PIPE,
            text=True
        )
        
        # Close the stdout stream of aws_proc in the parent process 
        # to prevent resource leakage (only needed if you plan to do more processing)
        aws_proc.stdout.close() 

        # Wait for the Helm command (helm_proc) to finish and capture its output
        helm_stdout, helm_stderr = helm_proc.communicate()
        
        # Wait for the AWS process to finish as well
        aws_proc.wait() 

        if aws_proc.returncode != 0:
            _, aws_stderr = aws_proc.communicate() 
            raise Exception(f"aws command to get password failed. Error: {aws_stderr}")
            
        if helm_proc.returncode == 0:
            logger.info("Login to helm registry was successful.")
            logger.info(helm_stdout.strip())
        else:
            raise Exception(f"Login to helm registry failed, Exit code: {helm_proc.returncode}, Error: {helm_stderr.strip()}")

    except FileNotFoundError as e:
        # This catches errors if 'aws' or 'helm' are not in the PATH
        raise Exception(f"Command not found. Please ensure '{e.filename}' is installed and in your system's PATH.")
    except Exception as e:
        raise Exception(f"An unexpected error occurred: {e}.")
    

def main():
    build_scenario = os.environ.get("BUILD_SCENARIO")
    build_info = load_build_info(build_scenario)


    registry = build_info.helm_charts["mongodb-kubernetes"].registry
    region = build_info.helm_charts["mongodb-kubernetes"].region
    return helm_registry_login(registry, region)

if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        logger.error(f"Failed while logging in to the helm registry. Error: {e}")
        sys.exit(1)