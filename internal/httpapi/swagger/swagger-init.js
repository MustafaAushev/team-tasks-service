window.addEventListener('load', function () {
  window.ui = SwaggerUIBundle({
    url: '/openapi.yaml',
    dom_id: '#swagger-ui',
    deepLinking: true,
    tryItOutEnabled: true,
    persistAuthorization: true,
    presets: [SwaggerUIBundle.presets.apis]
  });
});
