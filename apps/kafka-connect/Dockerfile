FROM confluentinc/cp-kafka-connect-base:7.7.1 AS connector-builder
RUN confluent-hub install --no-prompt wepay/kafka-connect-bigquery:latest

FROM quay.io/strimzi/kafka:0.43.0-kafka-3.8.0
USER root
RUN mkdir -p /opt/kafka/plugins
COPY --from=connector-builder /usr/share/confluent-hub-components/ /opt/kafka/plugins/
USER 1001
