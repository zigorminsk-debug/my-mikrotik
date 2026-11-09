package com.mikrotik.inspector;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.pm.PackageInfo;
import android.content.pm.PackageManager;
import android.content.pm.Signature;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.security.MessageDigest;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Set;
import java.util.regex.Pattern;

/** Checks official releases on app launch (at most daily) and on demand. */
final class UpdateChecker {
    private static final String REPO = "https://github.com/zigorminsk-debug/my-mikrotik/releases/download/";
    private static final String API = "https://api.github.com/repos/zigorminsk-debug/my-mikrotik/releases/latest";
    private static final long DAY = 24L * 60 * 60 * 1000;
    private static final int MAX_APK = 128 * 1024 * 1024;
    private static final Pattern TAG = Pattern.compile("v[0-9]+\\.[0-9]+\\.[0-9]+");
    private final Activity activity;
    private volatile boolean busy;
    private boolean awaitingPermission;

    UpdateChecker(Activity activity) { this.activity = activity; }

    void autoCheck() {
        long last = activity.getPreferences(Activity.MODE_PRIVATE).getLong("last_update_check", 0);
        if (System.currentTimeMillis() - last >= DAY) check(false);
    }

    synchronized void check(final boolean manual) {
        if (busy) { if (manual) toast("Проверка обновлений уже выполняется"); return; }
        busy = true;
        if (manual) toast("Проверяем обновления…");
        new Thread(() -> {
            try {
                JSONObject release = new JSONObject(new String(read(API, 1024 * 1024), java.nio.charset.StandardCharsets.UTF_8));
                String tag = release.getString("tag_name");
                if (!TAG.matcher(tag).matches() || release.optBoolean("prerelease") || release.optBoolean("draft")) throw new Exception("Недопустимая версия релиза");
                String installed = activity.getPackageManager().getPackageInfo(activity.getPackageName(), 0).versionName;
                if (installed == null || !installed.matches("v?[0-9]+\\.[0-9]+\\.[0-9]+(?:-.*)?")) throw new Exception("Установленная версия не поддерживает автоматическое сравнение");
                boolean newer = isNewer(tag, installed);
                String apkName = "mikrotik-inspector-" + tag.substring(1) + "-android-release.apk";
                String sumName = "SHA256SUMS-android.txt";
                JSONArray assets = release.getJSONArray("assets");
                boolean apkFound = false, sumFound = false;
                for (int i = 0; i < assets.length(); i++) {
                    JSONObject a = assets.getJSONObject(i);
                    String name = a.getString("name"), url = a.getString("browser_download_url");
                    if (name.equals(apkName) && url.equals(REPO + tag + "/" + apkName)) apkFound = true;
                    if (name.equals(sumName) && url.equals(REPO + tag + "/" + sumName)) sumFound = true;
                }
                final boolean available = apkFound && sumFound;
                if (!newer || available) activity.getPreferences(Activity.MODE_PRIVATE).edit().putLong("last_update_check", System.currentTimeMillis()).apply();
                synchronized (this) { busy = false; }
                onUI(() -> {
                    if (!newer) { if (manual) dialog("Обновления", "Установлена актуальная версия: " + installed); return; }
                    if (!available) { if (manual) dialog("Обновления", "Версия " + tag + " найдена, но подписанный release APK или контрольная сумма ещё не опубликованы."); return; }
                    new AlertDialog.Builder(activity).setTitle("Доступно обновление " + tag)
                        .setMessage("Скачать подписанный APK из GitHub? Android попросит подтвердить установку.")
                        .setNegativeButton("Позже", null)
                        .setPositiveButton("Скачать", (d, w) -> download(tag, apkName, sumName)).show();
                });
            } catch (Exception e) {
                synchronized (this) { busy = false; }
                if (manual) onUI(() -> dialog("Ошибка проверки обновлений", safeError(e)));
            }
        }, "update-check").start();
    }

    private synchronized void download(String tag, String apkName, String sumName) {
        if (busy) { toast("Проверка ещё выполняется, попробуйте снова"); return; }
        busy = true;
        toast("Загрузка обновления…");
        new Thread(() -> {
            File part = new File(activity.getCacheDir(), "update-pending.apk");
            File apk = new File(activity.getCacheDir(), "update.apk");
            try {
                String sums = new String(read(REPO + tag + "/" + sumName, 256 * 1024), java.nio.charset.StandardCharsets.UTF_8);
                String hash = null;
                for (String line : sums.split("\\r?\\n")) {
                    String[] p = line.trim().split("\\s+", 2);
                    if (p.length == 2 && p[1].replaceFirst("^\\*", "").equals(apkName) && p[0].matches("[0-9a-fA-F]{64}")) hash = p[0];
                }
                if (hash == null) throw new Exception("Контрольная сумма APK не найдена");
                MessageDigest sha = MessageDigest.getInstance("SHA-256");
                try (InputStream in = open(REPO + tag + "/" + apkName); FileOutputStream out = new FileOutputStream(part)) {
                    byte[] buf = new byte[32768]; int n, total = 0;
                    while ((n = in.read(buf)) != -1) { total += n; if (total > MAX_APK) throw new Exception("APK слишком велик"); sha.update(buf, 0, n); out.write(buf, 0, n); }
                }
                StringBuilder hex = new StringBuilder(); for (byte x : sha.digest()) hex.append(String.format(java.util.Locale.ROOT, "%02x", x & 255));
                if (!hex.toString().equalsIgnoreCase(hash)) throw new Exception("Контрольная сумма APK не совпадает");
                verifyPackage(part, tag);
                if (apk.exists() && !apk.delete()) throw new Exception("Не удалось заменить старый APK");
                if (!part.renameTo(apk)) throw new Exception("Не удалось сохранить APK");
                onUI(this::install);
            } catch (Exception e) { part.delete(); onUI(() -> dialog("Ошибка обновления", safeError(e))); }
            finally { synchronized (this) { busy = false; } }
        }, "update-download").start();
    }

    @SuppressWarnings("deprecation")
    private void verifyPackage(File file, String expectedTag) throws Exception {
        PackageManager pm = activity.getPackageManager();
        PackageInfo installed = pm.getPackageInfo(activity.getPackageName(), PackageManager.GET_SIGNATURES);
        PackageInfo candidate = pm.getPackageArchiveInfo(file.getAbsolutePath(), PackageManager.GET_SIGNATURES);
        if (candidate == null || !activity.getPackageName().equals(candidate.packageName)) throw new Exception("APK не является обновлением этого приложения");
        if (expectedTag != null && !expectedTag.substring(1).equals(candidate.versionName)) throw new Exception("Версия APK не совпадает с релизом");
        long oldCode = Build.VERSION.SDK_INT >= 28 ? installed.getLongVersionCode() : installed.versionCode;
        long newCode = Build.VERSION.SDK_INT >= 28 ? candidate.getLongVersionCode() : candidate.versionCode;
        if (newCode <= oldCode) throw new Exception("Версия APK не новее установленной");
        if (installed.signatures == null || candidate.signatures == null) throw new Exception("Не удалось проверить подпись APK");
        Set<Signature> a = new HashSet<>(Arrays.asList(installed.signatures));
        Set<Signature> b = new HashSet<>(Arrays.asList(candidate.signatures));
        if (a.isEmpty() || !a.equals(b)) throw new Exception("Подпись APK отличается от установленной версии. Требуется выпуск с тем же ключом подписи.");
    }

    private void install() {
        try {
            File file = new File(activity.getCacheDir(), "update.apk"); verifyPackage(file, null);
            if (Build.VERSION.SDK_INT >= 26 && !activity.getPackageManager().canRequestPackageInstalls()) {
                new AlertDialog.Builder(activity).setTitle("Разрешение на установку")
                    .setMessage("Разрешите установку обновлений из этого приложения, затем вернитесь назад.")
                    .setNegativeButton("Отмена", null).setPositiveButton("Открыть настройки", (d, w) -> {
                        awaitingPermission = true;
                        activity.startActivity(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + activity.getPackageName())));
                    }).show();
                return;
            }
            Uri uri = Uri.parse("content://" + activity.getPackageName() + ".updates/update.apk");
            Intent intent = new Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive");
            intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            activity.startActivity(intent);
        } catch (Exception e) { dialog("Ошибка установки", safeError(e)); }
    }
    void onResume() { if (awaitingPermission) { awaitingPermission = false; if (Build.VERSION.SDK_INT >= 26 && activity.getPackageManager().canRequestPackageInstalls()) install(); } }

    static boolean isNewer(String tag, String current) {
        if (current == null) return false;
        String c = current.startsWith("v") ? current.substring(1) : current;
        String base = c.split("-", 2)[0];
        String[] a = tag.substring(1).split("\\."), b = base.split("\\.");
        if (a.length != 3 || b.length != 3) return false;
        try {
            for (int i = 0; i < 3; i++) { long x = Long.parseLong(a[i]), y = Long.parseLong(b[i]); if (x != y) return x > y; }
            return c.contains("-");
        } catch (NumberFormatException e) { return false; }
    }

    private static InputStream open(String address) throws Exception {
        URL url = new URL(address);
        for (int i = 0; i < 6; i++) {
            String host = url.getHost().toLowerCase(java.util.Locale.ROOT);
            if (!"https".equals(url.getProtocol()) || !(host.equals("github.com") || host.equals("api.github.com") || host.equals("release-assets.githubusercontent.com") || host.equals("objects.githubusercontent.com"))) throw new Exception("Недоверенный адрес обновления");
            HttpURLConnection c = (HttpURLConnection) url.openConnection(); c.setInstanceFollowRedirects(false); c.setConnectTimeout(15000); c.setReadTimeout(30000); c.setRequestProperty("User-Agent", "MikroTik-Inspector"); c.setRequestProperty("Accept", "application/vnd.github+json");
            int status = c.getResponseCode();
            if (status >= 300 && status < 400) { String next = c.getHeaderField("Location"); c.disconnect(); if (next == null) throw new Exception("Пустой адрес перенаправления"); url = new URL(url, next); continue; }
            if (status != 200) { c.disconnect(); throw new Exception("Сервер вернул HTTP " + status); }
            return c.getInputStream();
        }
        throw new Exception("Слишком много перенаправлений");
    }
    private static byte[] read(String url, int limit) throws Exception {
        try (InputStream in = open(url); ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buf = new byte[8192]; int n, total = 0;
            while ((n = in.read(buf)) != -1) { total += n; if (total > limit) throw new Exception("Ответ слишком велик"); out.write(buf, 0, n); }
            return out.toByteArray();
        }
    }
    private void onUI(Runnable action) { activity.runOnUiThread(() -> { if (!activity.isFinishing() && !activity.isDestroyed()) action.run(); }); }
    private void dialog(String title, String text) { new AlertDialog.Builder(activity).setTitle(title).setMessage(text).setPositiveButton("OK", null).show(); }
    private void toast(String text) { Toast.makeText(activity, text, Toast.LENGTH_SHORT).show(); }
    private static String safeError(Exception e) { return e.getMessage() != null ? e.getMessage() : e.getClass().getSimpleName(); }
}
