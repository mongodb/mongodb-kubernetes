#!/usr/bin/env python3

import json
import subprocess
import sys
import os
from typing import Set, List, Dict, Tuple

class ArchitectureVerifier:
    def __init__(self):
        self.aws_profile = "mms-scratch"
        self.registry_id = "268558157000"
        self.repository_name = "lucian.tosa/mongodb-agent"
        self.region = "us-east-1"
        self.required_archs = {"amd64", "arm64", "s390x", "ppc64le"}
        
        self.arch_descriptions = {
            "amd64": "x86_64/Intel/AMD 64-bit",
            "arm64": "ARM 64-bit", 
            "s390x": "IBM Z Systems",
            "ppc64le": "IBM Power Systems"
        }
    
    def run_aws_command(self, cmd: List[str]) -> subprocess.CompletedProcess:
        """Run AWS CLI command with the configured profile"""
        env = os.environ.copy()
        env["AWS_PROFILE"] = self.aws_profile
        return subprocess.run(cmd, capture_output=True, text=True, env=env)
    
    def get_container_images(self) -> List[Dict]:
        """Get all container images (excluding signature files)"""
        cmd = [
            "aws", "ecr", "describe-images",
            "--registry-id", self.registry_id,
            "--repository-name", self.repository_name,
            "--region", self.region,
            "--output", "json"
        ]
        
        result = self.run_aws_command(cmd)
        if result.returncode != 0:
            raise Exception(f"Failed to fetch images: {result.stderr}")
        
        data = json.loads(result.stdout)
        
        # Filter out signature files and get container images
        container_images = []
        for image in data.get("imageDetails", []):
            image_tags = image.get("imageTags", [])
            if not image_tags:
                continue
            if any(tag.endswith(".sig") for tag in image_tags):
                continue
            container_images.append({
                "tag": image_tags[0],
                "digest": image.get("imageDigest", ""),
                "mediaType": image.get("imageManifestMediaType", "") or image.get("artifactMediaType", "")
            })
        
        return container_images
    
    def get_manifest_architectures(self, tag: str) -> Tuple[List[str], str]:
        """Get actual architectures from a manifest"""
        cmd = [
            "aws", "ecr", "batch-get-image",
            "--registry-id", self.registry_id,
            "--repository-name", self.repository_name,
            "--region", self.region,
            "--image-ids", f"imageTag={tag}",
            "--query", "images[0].imageManifest",
            "--output", "text"
        ]
        
        try:
            result = self.run_aws_command(cmd)
            if result.returncode != 0:
                return [], f"Failed to get manifest: {result.stderr.strip()}"
            
            manifest = json.loads(result.stdout)
            if "manifests" in manifest:
                archs = sorted([m["platform"]["architecture"] for m in manifest["manifests"]])
                return archs, ""
            else:
                return [], "Could not parse manifest - no manifests found"
                
        except Exception as e:
            return [], f"Error: {e}"
    
    def analyze_image(self, image: Dict) -> Dict:
        """Analyze a single image for architecture support"""
        tag = image["tag"]
        media_type = image["mediaType"]
        
        analysis = {
            "tag": tag,
            "digest": image["digest"],
            "type": "unknown",
            "architectures": [],
            "error": None,
            "missing_archs": set(),
            "has_all_required": False
        }
        
        print(f"🔍 Checking {tag}...")
        
        if ("manifest.list" in media_type or 
            media_type == "application/vnd.docker.distribution.manifest.list.v2+json"):
            
            # Multi-arch manifest
            archs, error = self.get_manifest_architectures(tag)
            
            if error:
                analysis.update({
                    "type": "multi-arch-error",
                    "error": error
                })
                print(f"  ❌ {error}")
            else:
                analysis.update({
                    "type": "multi-arch",
                    "architectures": archs
                })
                
                present_archs = set(archs)
                missing_archs = self.required_archs - present_archs
                analysis["missing_archs"] = missing_archs
                analysis["has_all_required"] = len(missing_archs) == 0
                
                print(f"  📦 Multi-arch manifest: {', '.join(archs)}")
                
                if missing_archs:
                    print(f"  ❌ Missing: {', '.join(sorted(missing_archs))}")
                else:
                    print("  ✅ All required architectures present")
        else:
            # Single arch or unknown
            analysis.update({
                "type": "single-arch",
                "error": "Architecture info not available in ECR metadata"
            })
            print("  🏗️  Single architecture image (architecture info not available in ECR metadata)")
        
        print()
        return analysis
    
    def print_summary(self, analyses: List[Dict]) -> bool:
        """Print summary and return True if all architectures are found"""
        print("=== SUMMARY ===")
        print()
        
        # Collect all found architectures
        all_found_archs = set()
        images_with_issues = []
        
        for analysis in analyses:
            if analysis["architectures"]:
                all_found_archs.update(analysis["architectures"])
            
            if analysis["error"] or not analysis["has_all_required"]:
                if analysis["error"]:
                    images_with_issues.append(f"{analysis['tag']}: {analysis['error']}")
                if analysis["missing_archs"]:
                    missing_str = ", ".join(sorted(analysis["missing_archs"]))
                    images_with_issues.append(f"{analysis['tag']}: missing {missing_str}")
        
        # Print found architectures
        print("📊 ARCHITECTURES FOUND ACROSS ALL IMAGES:")
        if all_found_archs:
            for arch in sorted(all_found_archs):
                desc = self.arch_descriptions.get(arch, arch)
                print(f"  ✅ {arch} ({desc})")
        else:
            print("  ❌ No architecture information found")
        
        print()
        
        # Print missing architectures
        print("❌ MISSING REQUIRED ARCHITECTURES:")
        missing_archs = self.required_archs - all_found_archs
        
        if missing_archs:
            for arch in sorted(missing_archs):
                desc = self.arch_descriptions.get(arch, arch)
                print(f"  ❌ {arch} ({desc}): Not found in any image")
        else:
            print("  🎉 ALL REQUIRED ARCHITECTURES FOUND!")
        
        # Print images with issues
        if images_with_issues:
            print()
            print("⚠️  IMAGES WITH ISSUES:")
            for issue in images_with_issues:
                print(f"  • {issue}")
        
        return len(missing_archs) == 0
    
    def print_final_result(self, success: bool, analyses: List[Dict]):
        """Print final verification result"""
        print()
        print("=== FINAL RESULT ===")
        
        if success:
            print("🎉 SUCCESS: All required architectures (amd64, arm64, s390x, ppc64le) are available!")
            print("✨ Multi-architecture manifests properly configured!")
            print()
            
            # Check for single-arch images
            single_arch_count = len([a for a in analyses if a["type"] == "single-arch"])
            if single_arch_count > 0:
                print(f"ℹ️  Note: {single_arch_count} images appear to be single-architecture.")
                print("   This is not necessarily a problem if multi-arch images are available.")
            
            print("✅ VERIFICATION COMPLETE: MongoDB agents support all required architectures")
            return True
        else:
            all_found_archs = set()
            for analysis in analyses:
                if analysis["architectures"]:
                    all_found_archs.update(analysis["architectures"])
            
            missing_archs = self.required_archs - all_found_archs
            print(f"❌ FAILURE: Missing architectures - {', '.join(sorted(missing_archs))}")
            print()
            print("🚨 These architectures are not available in any image version:")
            
            for arch in sorted(missing_archs):
                if arch == "amd64":
                    print(f"  • {arch}: Cannot deploy on standard x86_64 servers")
                elif arch == "arm64":
                    print(f"  • {arch}: Cannot deploy on ARM-based systems")
                elif arch == "s390x":
                    print(f"  • {arch}: Cannot deploy on IBM Z mainframe systems")
                elif arch == "ppc64le":
                    print(f"  • {arch}: Cannot deploy on IBM Power servers")
                else:
                    print(f"  • {arch}: Cannot deploy on this architecture")
            
            print()
            print("❌ VERIFICATION FAILED: Critical architectures missing")
            return False
    
    def verify(self) -> bool:
        """Main verification method"""
        print("=== MongoDB Agent Architecture Verification ===")
        print(f"Repository: {self.registry_id}.dkr.ecr.{self.region}.amazonaws.com/{self.repository_name}")
        print(f"Required architectures: {', '.join(sorted(self.required_archs))}")
        print()
        
        try:
            # Get container images
            print("📥 Fetching images and checking actual architectures...")
            print()
            
            container_images = self.get_container_images()
            print(f"📊 Found {len(container_images)} container images (excluding signature files)")
            print()
            
            # Analyze each image
            analyses = []
            for image in container_images:
                analysis = self.analyze_image(image)
                analyses.append(analysis)
            
            # Print summary
            success = self.print_summary(analyses)
            
            # Print final result
            return self.print_final_result(success, analyses)
            
        except Exception as e:
            print(f"❌ ERROR: {e}")
            return False


def main():
    """Main entry point"""
    verifier = ArchitectureVerifier()
    success = verifier.verify()
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()