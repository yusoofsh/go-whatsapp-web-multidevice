# Release verification

The image workflow now owns the regression gate previously in `sync-validation.yml`. It checks the module graph and transport formatting, runs the full Go tests and vet, and builds the application before building a runtime candidate.

The exact candidate is checked as UID 20001 without networking, saved sessions, volumes, added capabilities or a writable root filesystem. The checks prove that the packaged executable starts its help command and that the packaged FFmpeg binary loads. They do not connect a WhatsApp account or prove live OAuth, event delivery, archive completeness or a production deployment.

Only current-main builds can authenticate to the registry and promote the already-tested image to `latest`. The source revision label must match the workflow commit. Pull requests exercise the same build and smoke checks but do not publish. There is no rebuild between the smoke check and publication.
