# Static UI served by nginx. No build step — Tailwind comes from a CDN and
# the rest is vanilla JS. The image is just nginx:alpine with our files +
# config copied in.

FROM nginx:1.27-alpine

# Strip the default config so we own /etc/nginx/conf.d/*.conf entirely.
RUN rm /etc/nginx/conf.d/default.conf

COPY nginx.conf /etc/nginx/conf.d/vmaas.conf
COPY index.html /usr/share/nginx/html/index.html
COPY app.js     /usr/share/nginx/html/app.js
COPY styles.css /usr/share/nginx/html/styles.css

EXPOSE 80
