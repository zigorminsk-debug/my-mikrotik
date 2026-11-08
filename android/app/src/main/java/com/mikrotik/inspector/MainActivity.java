package com.mikrotik.inspector;

import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.view.KeyEvent;
import android.view.ViewGroup;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;
import android.util.Log;

import com.mikrotik.inspector.bridge.Bridge;

/**
 * Главная (и единственная) активность приложения.
 *
 * Работает так же, как настольная версия: Go-ядро поднимает локальный
 * HTTP-сервер и отдаёт HTML-интерфейс, а WebView показывает страницу
 * http://127.0.0.1:8765/. Все запросы к роутеру (RouterOS API/REST)
 * выполняет Go внутри приложения — WebView только отображает интерфейс.
 */
public class MainActivity extends Activity {

    /** Порт локального сервера; менять не нужно. */
    public static final int PORT = 8765;

    private static final String TAG = "MikroTikInspector";

    private WebView webView;
    private TextView status;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(0xFF0F172A);

        webView = new WebView(this);
        webView.setBackgroundColor(0xFFEEF2F7);
        root.addView(webView, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT));

        status = new TextView(this);
        status.setTextColor(0xFFCBD5E1);
        status.setTextSize(14f);
        status.setPadding(32, 32, 32, 32);
        status.setText("Запуск MikroTik Inspector…");
        root.addView(status, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT));

        setContentView(root);
        configureWebView();

        // Go-сервер запускается в фоновом потоке: gomobile-вызовы нельзя
        // выполнять в UI-потоке.
        final String error = Bridge.start(PORT);
        if (error != null && !error.isEmpty()) {
            Log.e(TAG, "Bridge.start: " + error);
            status.setText("Не удалось запустить встроенный сервер:\n" + error);
            return;
        }

        String url = Bridge.url(PORT);
        Log.i(TAG, "Интерфейс: " + url);
        status.setVisibility(android.view.View.GONE);
        webView.loadUrl(url);
    }

    @SuppressLint("SetJavaScriptEnabled")
    private void configureWebView() {
        WebSettings s = webView.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);      // сохранение адреса/логина между запусками
        s.setDatabaseEnabled(true);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        s.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        s.setTextZoom(100);
        s.setSupportZoom(true);
        s.setBuiltInZoomControls(false);

        webView.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                // Всё, что не наш локальный сервер, открываем во внешнем браузере.
                Uri uri = request.getUrl();
                if (uri.getHost() != null && uri.getHost().startsWith("127.0.0.1")) {
                    return false;
                }
                try {
                    startActivity(new Intent(Intent.ACTION_VIEW, uri));
                } catch (Exception ignored) {
                    // Нет приложения для такого адреса — просто не открываем.
                }
                return true;
            }
        });
    }

    @Override
    public boolean onKeyDown(int keyCode, KeyEvent event) {
        // Кнопка «Назад» внутри интерфейса не должна закрывать приложение сразу.
        if (keyCode == KeyEvent.KEYCODE_BACK && webView != null && webView.canGoBack()) {
            webView.goBack();
            return true;
        }
        return super.onKeyDown(keyCode, event);
    }

    @Override
    protected void onDestroy() {
        if (webView != null) {
            webView.destroy();
            webView = null;
        }
        super.onDestroy();
    }
}
