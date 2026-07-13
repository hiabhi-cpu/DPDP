#!/usr/bin/env bash
# Generates dev mTLS material for integration-service:
#   ca.pem/ca.key        — the hospital CA (server trusts client certs it signs)
#   server.pem/server.key — the webhook server cert (CN=localhost)
#   hosp-1.pem/hosp-1.key — a test hospital CLIENT cert (CN = hospital_id)
# The CN of a client cert IS the hospital_id the webhook will trust.
# Run:  bash integration-service/certs/gen-dev-certs.sh
set -euo pipefail
cd "$(dirname "$0")"

HOSPITAL_ID="${1:-hosp-1}"

# CA
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=dpdp-dev-hospital-ca" -out ca.pem

# Server cert (CN=localhost, SAN localhost + 127.0.0.1)
openssl ecparam -name prime256v1 -genkey -noout -out server.key
openssl req -new -key server.key -subj "/CN=localhost" -out server.csr
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 -sha256 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth") \
  -out server.pem

# Client cert for one hospital (CN = hospital_id)
openssl ecparam -name prime256v1 -genkey -noout -out "${HOSPITAL_ID}.key"
openssl req -new -key "${HOSPITAL_ID}.key" -subj "/CN=${HOSPITAL_ID}" -out "${HOSPITAL_ID}.csr"
openssl x509 -req -in "${HOSPITAL_ID}.csr" -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 -sha256 \
  -extfile <(printf "extendedKeyUsage=clientAuth") \
  -out "${HOSPITAL_ID}.pem"

rm -f ./*.csr ./*.srl
echo "Generated CA, server, and client cert for hospital_id=${HOSPITAL_ID} in $(pwd)"
