#!/usr/bin/env bash
#
# migrate-images-to-lfs.sh - Migrate existing bead images to Git LFS
#
# This script converts existing .beads/images/** to Git LFS tracking.
# It rewrites git history, so coordinate with your team before running.
#
# Prerequisites:
# - Git LFS installed (brew install git-lfs / apt install git-lfs)
# - All team members should have git-lfs installed before pushing
#
# Usage:
#   ./scripts/migrate-images-to-lfs.sh
#
# After running, you'll need to force push:
#   git push --force-with-lease --all
#   git push --force-with-lease --tags
#

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}=== Bead Images to Git LFS Migration ===${NC}"
echo

# Check if git-lfs is installed
if ! command -v git-lfs &> /dev/null; then
    echo -e "${RED}Error: git-lfs is not installed.${NC}"
    echo "Install it with:"
    echo "  brew install git-lfs   # macOS"
    echo "  apt install git-lfs    # Ubuntu/Debian"
    exit 1
fi

# Check if we're in a git repo
if ! git rev-parse --git-dir &> /dev/null; then
    echo -e "${RED}Error: Not in a git repository.${NC}"
    exit 1
fi

# Check for uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo -e "${RED}Error: You have uncommitted changes. Commit or stash them first.${NC}"
    exit 1
fi

# Show current image stats
echo -e "${YELLOW}Current .beads/images/ statistics:${NC}"
if [ -d ".beads/images" ]; then
    image_count=$(find .beads/images -type f \( -name "*.png" -o -name "*.jpg" -o -name "*.jpeg" -o -name "*.gif" -o -name "*.webp" -o -name "*.bmp" -o -name "*.svg" \) 2>/dev/null | wc -l | tr -d ' ')
    image_size=$(du -sh .beads/images 2>/dev/null | cut -f1)
    echo "  Files: $image_count"
    echo "  Size: $image_size"
else
    echo "  No .beads/images directory found"
fi
echo

# Confirm with user
echo -e "${YELLOW}This will:${NC}"
echo "  1. Install Git LFS hooks for this repo"
echo "  2. Update .gitattributes to track images with LFS"
echo "  3. Migrate existing images in git history to LFS"
echo
echo -e "${RED}WARNING: This rewrites git history!${NC}"
echo "After this, you must force push and all team members must re-clone or run:"
echo "  git lfs fetch --all && git lfs pull"
echo
read -p "Continue? [y/N] " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 0
fi

echo
echo -e "${GREEN}Step 1: Installing Git LFS hooks...${NC}"
git lfs install

echo
echo -e "${GREEN}Step 2: Verifying .gitattributes...${NC}"
# Check if .gitattributes already has LFS rules for images
if grep -q ".beads/images/\*\*/\*.png filter=lfs" .gitattributes 2>/dev/null; then
    echo "  LFS rules already present in .gitattributes"
else
    echo "  Adding LFS rules to .gitattributes..."
    cat >> .gitattributes << 'EOF'

# Track bead images with Git LFS to prevent repository bloat
# Images are lazy-loaded on access rather than downloaded on clone
.beads/images/**/*.png filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.jpg filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.jpeg filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.gif filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.webp filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.bmp filter=lfs diff=lfs merge=lfs -text
.beads/images/**/*.svg filter=lfs diff=lfs merge=lfs -text
EOF
    git add .gitattributes
    git commit -m "chore: add Git LFS tracking for bead images"
fi

echo
echo -e "${GREEN}Step 3: Migrating existing images to LFS...${NC}"
echo "  This may take a while depending on your git history size..."

# Migrate images in history
# Use --include-ref to specify which refs to migrate
git lfs migrate import --include=".beads/images/**" --everything --yes

echo
echo -e "${GREEN}=== Migration Complete ===${NC}"
echo
echo "Next steps:"
echo "  1. Verify the migration:"
echo "     git lfs ls-files"
echo
echo "  2. Force push to remote (coordinate with team first!):"
echo "     git push --force-with-lease --all"
echo "     git push --force-with-lease --tags"
echo
echo "  3. Team members should re-clone or run:"
echo "     git fetch origin"
echo "     git reset --hard origin/<branch>"
echo "     git lfs pull"
echo
echo "  4. To reclaim space after cleanup commits are pushed:"
echo "     git lfs prune --verify-remote"
echo
echo -e "${GREEN}Done!${NC}"
