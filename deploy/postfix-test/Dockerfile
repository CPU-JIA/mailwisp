FROM alpine:3.24.0@sha256:33154315cf4402e697f065e6ec2156e292187e633908ccfede9c66279b6fa956

RUN apk add --no-cache postfix=3.11.5-r0 \
    && test "$(postconf -h mail_version)" = "3.11.5" \
    && apk info --exists 'postfix=3.11.5-r0'

COPY main.cf /etc/postfix/main.cf
COPY entrypoint.sh /usr/local/bin/mailwisp-postfix-test

RUN chmod 0755 /usr/local/bin/mailwisp-postfix-test \
    && postfix check

EXPOSE 25

ENTRYPOINT ["/usr/local/bin/mailwisp-postfix-test"]
