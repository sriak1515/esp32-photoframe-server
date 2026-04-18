import '@mdi/font/css/materialdesignicons.css';
import 'vuetify/styles';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';

export default createVuetify({
  components,
  directives,
  theme: {
    defaultTheme: 'light',
    themes: {
      light: {
        colors: {
          primary: '#ce9160',
          secondary: '#424242',
          accent: '#82B1FF',
          error: '#982f2f',
          info: '#2f6398',
          success: '#2f9852',
          warning: '#987e2f',
          background: '#F5F5F5',
        },
      },
    },
  },
});
