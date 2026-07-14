FROM mcr.microsoft.com/playwright:v1.54.1-jammy

WORKDIR /app

COPY package.json package-lock.json* ./
RUN npm install --omit=dev

COPY server.mjs ./

ENV BROWSER_GATEWAY_HOST=0.0.0.0
ENV BROWSER_GATEWAY_PORT=18789
ENV BROWSER_GATEWAY_DATA_DIR=/data
ENV BROWSER_ENGINE_PACKAGE=cloakbrowser

EXPOSE 18789

CMD ["node", "server.mjs"]
