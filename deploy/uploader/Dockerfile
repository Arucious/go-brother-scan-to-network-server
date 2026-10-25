FROM python:3.12-alpine@sha256:1887c114801a8c82a4ec01daa52cfe7fc3f63573640e2247320289807ac1c3bb
COPY --chmod=0555 rclone /usr/local/bin/rclone
COPY --chmod=0555 upload.py /app/upload.py
USER 1000:1000
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 HOME=/tmp
ENTRYPOINT ["python3", "/app/upload.py"]
