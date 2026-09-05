FROM python:3.12.11-slim-bookworm
RUN useradd --create-home --uid 10001 --shell /usr/sbin/nologin verifier
COPY docker/verifier/ /opt/verifier/
RUN chmod -R a-w /opt/verifier
USER verifier
ENTRYPOINT ["python3", "/opt/verifier/verify.py"]
