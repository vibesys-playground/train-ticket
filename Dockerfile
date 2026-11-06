# The Train Ticket application image of the train-ticket-k8s task
# (.vibesys/tasks/train-ticket-k8s). Its build context is the repository root, so the
# Maven reactor (the root pom.xml and ts-common) is part of it; `.dockerignore` keeps out
# what the build does not need, including any `target/` left by a local build.
#
# Every application pod of the task runs this one image. The task's manifest starts each
# service with `command: [<service>]`, so the image must have one executable per service on
# PATH: ts-config-service, ts-station-service, ts-train-service, ts-travel-service,
# ts-route-service and ts-price-service. Each listens on its usual port and reaches its
# MongoDB at ts-<name>-mongo:27017 (see the task's OBJECTIVE.md). How they are built, and in
# what language, is the candidate's to change.

FROM maven:3.8.8-eclipse-temurin-8@sha256:daa5d635ffdeb1a3c31d12079b6829115214701828e43d61e4e7f6a61640e62c AS build
WORKDIR /src
COPY . .
ENV SERVICES="ts-config-service ts-station-service ts-train-service ts-travel-service ts-route-service ts-price-service"
# The Maven cache is a BuildKit cache mount: kept by the builder across builds, never in the image.
RUN --mount=type=cache,target=/root/.m2 \
    mvn -B -q -pl "$(echo $SERVICES | tr ' ' ',')" -am package -Dmaven.test.skip=true \
 && mkdir -p /out/bin /out/app \
 && for service in $SERVICES; do \
      cp "$service/target/$service-1.0.jar" "/out/app/$service.jar" \
      && printf '#!/bin/sh\nexec java $JAVA_OPTS -Xmx200m -jar /app/%s.jar "$@"\n' "$service" > "/out/bin/$service" \
      && chmod 0755 "/out/bin/$service"; \
    done

FROM eclipse-temurin:8-jre@sha256:377fe8e36a4b1530d9d52020069367ef44313fa891a82ae102b1016a6a3a72f5
ENV TZ=Asia/Shanghai
COPY --from=build /out/app/ /app/
COPY --from=build /out/bin/ /usr/local/bin/
