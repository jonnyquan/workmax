go env -w GOOS=linux GOARCH=amd64
go build

scp -r server root@WorkMaxAI:/www/wwwroot/workmax.app/server/deploy/

# ---------------------------------------------------------------------------
# Production server runtime requirements (one-time setup; verify after
# every fresh server provision):
#
#   sudo apt-get install -y python3 python3-pip
#   sudo pip3 install python-pptx
#
# These are needed by skills/<skill>/scripts/*.py — post-generation
# validators the agent runtime invokes via subprocess after a turn
# completes. Currently used by:
#
#   skills/ppt/scripts/validate-pptx.py
#     Structural + content check on generated .pptx outputs.
#     Without python-pptx installed the script falls back to
#     structural-only (still useful: catches corrupt zips, zero-slide
#     files); without python3 at all the validator returns "skipped"
#     and the user-visible flow continues unchanged.
#
# Verify on the server:
#
#   python3 -c 'import pptx; print(pptx.__version__)'
#
# Reaching the "(missing)" branch is non-fatal — runtime degrades
# gracefully — but means we lose the per-turn quality signal in
# done events.
# ---------------------------------------------------------------------------
