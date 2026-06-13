FROM mcr.microsoft.com/playwright:v1.57.0-noble

WORKDIR /e2e

COPY package.json package-lock.json* ./
RUN npm install

COPY playwright.config.js ./
COPY helpers ./helpers
COPY flows ./flows

CMD ["npx", "playwright", "test"]
